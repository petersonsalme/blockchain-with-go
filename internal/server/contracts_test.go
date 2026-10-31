package server

import (
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/petersonsalme/blockchain-with-go/internal/consensus/pow"
	"github.com/petersonsalme/blockchain-with-go/internal/domain"
	"github.com/petersonsalme/blockchain-with-go/internal/storage/memory"
	"github.com/petersonsalme/blockchain-with-go/internal/vm"
)

var _ = DescribeTable("respondWithSubmitError for contract errors",
	func(err error, status int, code string) {
		rec := httptest.NewRecorder()
		respondWithSubmitError(rec, fmt.Errorf("transaction 0: %w", err))

		Expect(rec.Code).To(Equal(status))

		var body errorResponse
		Expect(json.Unmarshal(rec.Body.Bytes(), &body)).To(Succeed())
		Expect(body.Code).To(Equal(code))
	},
	Entry("invalid contract operation", domain.ErrInvalidContractOp, http.StatusBadRequest, "invalid_contract_op"),
	Entry("invalid code", vm.ErrInvalidCode, http.StatusBadRequest, "invalid_code"),
	Entry("unknown contract", vm.ErrUnknownContract, http.StatusUnprocessableEntity, "unknown_contract"),
	Entry("contract exists", vm.ErrContractExists, http.StatusUnprocessableEntity, "contract_exists"),
	Entry("out of gas", vm.ErrOutOfGas, http.StatusUnprocessableEntity, "out_of_gas"),
	Entry("reverted", vm.ErrReverted, http.StatusUnprocessableEntity, "reverted"),
	Entry("stack overflow", vm.ErrStackOverflow, http.StatusUnprocessableEntity, "stack_overflow"),
	Entry("stack underflow", vm.ErrStackUnderflow, http.StatusUnprocessableEntity, "stack_underflow"),
	Entry("bad jump", vm.ErrBadJump, http.StatusUnprocessableEntity, "bad_jump"),
	Entry("argument index", vm.ErrArgIndex, http.StatusUnprocessableEntity, "arg_index"),
	Entry("invalid gas", vm.ErrInvalidGas, http.StatusUnprocessableEntity, "invalid_gas"),
)

// nilStorageChain deploys a contract whose storage map is nil.
type nilStorageChain struct{ brokenChain }

func (nilStorageChain) Contract(string) ([]byte, map[uint64]uint64, bool) {
	return []byte{0}, nil, true
}

var _ = Describe("GET /contracts/{id} with a nil storage", func() {
	It("answers an empty object, never null", func() {
		srv, err := NewServer(nilStorageChain{}, Config{})
		Expect(err).NotTo(HaveOccurred())

		rec := httptest.NewRecorder()

		srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/contracts/"+zeroHash, nil))

		Expect(rec.Code).To(Equal(http.StatusOK))
		Expect(rec.Body.String()).To(ContainSubstring(`"storage": {}`))
	})
})

var _ = Describe("Contract routes", func() {
	var (
		funds  *bank
		router http.Handler
	)

	BeforeEach(func() {
		funds = newBank()
		svc, err := funds.service(memory.New(), pow.NewProofOfWork(1))
		Expect(err).NotTo(HaveOccurred())
		srv, err := NewServer(svc, Config{})
		Expect(err).NotTo(HaveOccurred())

		router = srv.Handler()
	})

	do := func(method, path, body string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, httptest.NewRequest(method, path, strings.NewReader(body)))

		return rec
	}

	// contractTx builds a signed transaction and returns its wire JSON with the
	// lower-case contract object of the API.
	contractTx := func(op *domain.ContractOp) (id, body string) {
		tx := funds.tx(newWallet().Address(), 10)
		tx.Contract = op
		tx.ID = tx.CalculateHash()
		Expect(domain.SignInput(&tx, 0, funds.owner)).To(Succeed())

		args, _ := json.Marshal(op.Args)
		if op.Args == nil {
			args = []byte("null")
		}

		raw, err := json.Marshal(tx)
		Expect(err).NotTo(HaveOccurred())

		var m map[string]json.RawMessage
		Expect(json.Unmarshal(raw, &m)).To(Succeed())
		delete(m, "Contract")
		m["contract"] = json.RawMessage(fmt.Sprintf(`{"kind":%q,"code":%q,"target":%q,"args":%s,"gas_limit":%d}`,
			op.Kind, op.Code, op.Target, args, op.GasLimit))
		out, err := json.Marshal(m)
		Expect(err).NotTo(HaveOccurred())

		return tx.ID, string(out)
	}

	deployCode := func(code []byte) string {
		id, body := contractTx(&domain.ContractOp{Kind: domain.ContractDeploy, Code: hex.EncodeToString(code), GasLimit: 100})
		rec := do(http.MethodPost, "/tx", body)
		Expect(rec.Code).To(Equal(http.StatusAccepted), rec.Body.String())
		Expect(do(http.MethodPost, "/mine", "").Code).To(Equal(http.StatusCreated))

		return id
	}

	deploy := func() string {
		return deployCode(vm.CounterCode)
	}

	call := func(id string, gas uint64) *httptest.ResponseRecorder {
		_, body := contractTx(&domain.ContractOp{Kind: domain.ContractCall, Target: id, GasLimit: gas, Args: []uint64{}})
		return do(http.MethodPost, "/tx", body)
	}

	type contractBody struct {
		ID      string            `json:"id"`
		Code    string            `json:"code"`
		Storage map[string]uint64 `json:"storage"`
	}

	It("deploys the counter, calls it three times and reads {\"0\": 3}", func() {
		id := deploy()

		rec := do(http.MethodGet, "/contracts/"+id, "")
		Expect(rec.Code).To(Equal(http.StatusOK), rec.Body.String())
		Expect(rec.Header().Get("Content-Type")).To(Equal("application/json"))
		Expect(rec.Body.String()).To(ContainSubstring(`"storage": {}`))

		for range 3 {
			Expect(call(id, 29).Code).To(Equal(http.StatusAccepted))
			Expect(do(http.MethodPost, "/mine", "").Code).To(Equal(http.StatusCreated))
		}

		rec = do(http.MethodGet, "/contracts/"+id, "")

		var got contractBody
		Expect(json.Unmarshal(rec.Body.Bytes(), &got)).To(Succeed())
		Expect(got).To(Equal(contractBody{ID: id, Code: hex.EncodeToString(vm.CounterCode), Storage: map[string]uint64{"0": 3}}))
	})

	It("rejects a call with gas_limit 28 with 422 out_of_gas", func() {
		id := deploy()

		rec := call(id, 28)

		Expect(rec.Code).To(Equal(http.StatusUnprocessableEntity), rec.Body.String())
		Expect(rec.Body.String()).To(ContainSubstring(`"code": "out_of_gas"`))
	})

	It("rejects a call to an unknown contract with 422 unknown_contract", func() {
		rec := call(zeroHash, 29)

		Expect(rec.Code).To(Equal(http.StatusUnprocessableEntity), rec.Body.String())
		Expect(rec.Body.String()).To(ContainSubstring(`"code": "unknown_contract"`))
	})

	push := func(v uint64) []byte { return binary.BigEndian.AppendUint64([]byte{vm.OpPUSH8}, v) }

	DescribeTable("rejects a call that fails in the VM with 422",
		func(code []byte, want string) {
			id := deployCode(code)

			rec := call(id, 100)

			Expect(rec.Code).To(Equal(http.StatusUnprocessableEntity), rec.Body.String())
			Expect(rec.Body.String()).To(ContainSubstring(`"code": "` + want + `"`))

			mempool := do(http.MethodGet, "/mempool", "")
			Expect(mempool.Code).To(Equal(http.StatusOK))
			Expect(mempool.Body.String()).To(ContainSubstring(`"transactions": []`))

			Expect(do(http.MethodGet, "/contracts/"+id, "").Body.String()).To(ContainSubstring(`"storage": {}`))
		},
		Entry("reverted", []byte{vm.OpREVERT}, "reverted"),
		Entry("bad jump", append(push(1000), vm.OpJUMP), "bad_jump"),
		Entry("stack underflow", []byte{vm.OpPOP}, "stack_underflow"),
		Entry("stack overflow", bytes.Repeat(push(1), vm.MaxStack+1), "stack_overflow"),
		Entry("argument index", append(push(0), vm.OpARG), "arg_index"),
	)

	It("refuses the replay of a mined deploy with 409", func() {
		_, body := contractTx(&domain.ContractOp{Kind: domain.ContractDeploy, Code: hex.EncodeToString(vm.CounterCode), GasLimit: 100})
		Expect(do(http.MethodPost, "/tx", body).Code).To(Equal(http.StatusAccepted))
		Expect(do(http.MethodPost, "/mine", "").Code).To(Equal(http.StatusCreated))

		rec := do(http.MethodPost, "/tx", body)

		// the funding output is spent now, so the replay is refused before the VM runs
		Expect(rec.Code).To(Equal(http.StatusConflict), rec.Body.String())
	})

	It("rejects a structurally invalid operation with 400 invalid_contract_op", func() {
		_, body := contractTx(&domain.ContractOp{Kind: "burn", GasLimit: 10})

		rec := do(http.MethodPost, "/tx", body)

		Expect(rec.Code).To(Equal(http.StatusBadRequest), rec.Body.String())
		Expect(rec.Body.String()).To(ContainSubstring(`"code": "invalid_contract_op"`))
	})

	It("rejects undeployable code with 400 invalid_code", func() {
		_, body := contractTx(&domain.ContractOp{Kind: domain.ContractDeploy, Code: "01", GasLimit: 10})

		rec := do(http.MethodPost, "/tx", body)

		Expect(rec.Code).To(Equal(http.StatusBadRequest), rec.Body.String())
		Expect(rec.Body.String()).To(ContainSubstring(`"code": "invalid_code"`))
	})

	It("treats a null contract as no contract", func() {
		tx := funds.tx(newWallet().Address(), 10)
		raw, err := json.Marshal(tx)
		Expect(err).NotTo(HaveOccurred())

		rec := do(http.MethodPost, "/tx", strings.TrimSuffix(string(raw), "}")+`,"contract":null}`)

		Expect(rec.Code).To(Equal(http.StatusAccepted), rec.Body.String())
		Expect(rec.Body.String()).To(ContainSubstring(tx.ID))
	})

	It("answers 400 invalid_contract_id for an ID that is not canonical hex", func() {
		for _, id := range []string{"xyz", strings.Repeat("A", 64), zeroHash[:62]} {
			rec := do(http.MethodGet, "/contracts/"+id, "")

			Expect(rec.Code).To(Equal(http.StatusBadRequest), id)
			Expect(rec.Body.String()).To(ContainSubstring(`"code": "invalid_contract_id"`))
		}
	})

	It("answers 404 contract_not_found for an unknown ID", func() {
		rec := do(http.MethodGet, "/contracts/"+zeroHash, "")

		Expect(rec.Code).To(Equal(http.StatusNotFound))
		Expect(rec.Body.String()).To(ContainSubstring(`"code": "contract_not_found"`))
	})

	It("answers 405 with Allow: GET, HEAD for other methods", func() {
		rec := do(http.MethodPost, "/contracts/"+zeroHash, "")

		Expect(rec.Code).To(Equal(http.StatusMethodNotAllowed))
		Expect(rec.Header().Get("Allow")).To(Equal("GET, HEAD"))
	})
})
