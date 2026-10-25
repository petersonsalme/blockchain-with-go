package domain_test

import (
	"encoding/hex"
	"encoding/json"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/petersonsalme/blockchain-with-go/internal/domain"
)

var _ = Describe("ContractOp", func() {
	ts := time.Date(2026, 10, 16, 20, 0, 0, 123456000, time.UTC)
	target := strings.Repeat("ab", 32)

	Describe("CalculateHash without a contract", func() {
		// Hashes taken from master before ContractOp existed.
		DescribeTable("is unchanged",
			func(tx domain.Transaction, want string) {
				Expect(tx.CalculateHash()).To(Equal(want))
			},
			Entry("one input, one output", domain.Transaction{
				Inputs:    []domain.TxIn{{PrevTxID: "aa", OutIndex: 0}},
				Outputs:   []domain.TxOut{{Amount: 100, Address: "bob"}},
				Timestamp: ts,
			}, "9693cdcc1a14fa87c5bada6873fd0d76d9f6e5b88cf4ccf0d5912a318a28d5e2"),
			Entry("two inputs, two outputs", domain.Transaction{
				Inputs:    []domain.TxIn{{PrevTxID: "aa", OutIndex: 1}, {PrevTxID: "bb", OutIndex: 2}},
				Outputs:   []domain.TxOut{{Amount: 7, Address: "alice"}, {Amount: 3, Address: "bob"}},
				Timestamp: ts.Add(time.Hour),
			}, "cb918e88a633d9d2f469e509a0dea11aecc925aecce9dbd56015a89b93b7e15c"),
			Entry("empty", domain.Transaction{Timestamp: ts},
				"e0357e1b565e98a8477724eef5d7f0c02380391dade265fa2ba6ce8f54d7d91a"),
		)
	})

	It("leaves a transaction without contract out of its JSON, so Block.Size is unchanged", func() {
		raw, err := json.Marshal(domain.Transaction{Timestamp: ts})
		Expect(err).NotTo(HaveOccurred())
		Expect(string(raw)).NotTo(ContainSubstring("Contract"))
	})

	Describe("CalculateHash with a contract", func() {
		var (
			tx domain.Transaction
			w  = seededWallet(1)
		)
		BeforeEach(func() {
			tx = domain.Transaction{
				Inputs:    []domain.TxIn{{PrevTxID: "aa", OutIndex: 0}},
				Outputs:   []domain.TxOut{{Amount: 100, Address: "bob"}},
				Timestamp: ts,
				Contract:  &domain.ContractOp{Kind: domain.ContractCall, Target: target, Args: []uint64{1, 2}, GasLimit: 50},
			}
		})

		It("matches the canonical JSON vector", func() {
			const payload = `{"inputs":[{"prev_txid":"aa","out_index":0}],"outputs":[{"amount":100,"address":"bob"}],` +
				`"timestamp":"2026-10-16T20:00:00.123456Z","contract":{"kind":"call","code":"","target":"` +
				`abababababababababababababababababababababababababababababababab","args":[1,2],"gas_limit":50}}`
			Expect(tx.CalculateHash()).To(Equal(sha256Hex(payload)))
		})

		It("differs from the same transaction without a contract", func() {
			plain := tx
			plain.Contract = nil
			Expect(tx.CalculateHash()).NotTo(Equal(plain.CalculateHash()))
		})

		It("hashes nil and empty Args alike", func() {
			tx.Contract.Args = nil
			nilHash := tx.CalculateHash()
			tx.Contract.Args = []uint64{}
			Expect(tx.CalculateHash()).To(Equal(nilHash))
		})

		DescribeTable("changes the hash and breaks the signature when a field changes",
			func(mutate func(op *domain.ContractOp)) {
				Expect(domain.SignInput(&tx, 0, w)).To(Succeed())
				spent := domain.TxOut{Address: w.Address()}
				Expect(domain.VerifyInputSignature(&tx, 0, spent)).To(Succeed())
				before := tx.CalculateHash()

				mutate(tx.Contract)

				Expect(tx.CalculateHash()).NotTo(Equal(before))
				Expect(domain.VerifyInputSignature(&tx, 0, spent)).To(MatchError(domain.ErrInvalidSignature))
			},
			Entry("Kind", func(op *domain.ContractOp) { op.Kind = domain.ContractDeploy }),
			Entry("Code", func(op *domain.ContractOp) { op.Code = "00" }),
			Entry("Target", func(op *domain.ContractOp) { op.Target = strings.Repeat("cd", 32) }),
			Entry("Args value", func(op *domain.ContractOp) { op.Args[1] = 3 }),
			Entry("Args length", func(op *domain.ContractOp) { op.Args = op.Args[:1] }),
			Entry("GasLimit", func(op *domain.ContractOp) { op.GasLimit = 51 }),
		)
	})

	Describe("ValidateContractOp", func() {
		validDeploy := func() *domain.ContractOp {
			return &domain.ContractOp{Kind: domain.ContractDeploy, Code: "0100", GasLimit: 1}
		}
		validCall := func() *domain.ContractOp {
			return &domain.ContractOp{Kind: domain.ContractCall, Target: target, GasLimit: domain.MaxContractGas}
		}

		It("accepts nil", func() {
			Expect(domain.ValidateContractOp(nil)).To(Succeed())
		})
		It("accepts a deploy and a call", func() {
			Expect(domain.ValidateContractOp(validDeploy())).To(Succeed())
			Expect(domain.ValidateContractOp(validCall())).To(Succeed())
		})
		It("accepts code of exactly MaxContractCodeBytes", func() {
			op := validDeploy()
			op.Code = hex.EncodeToString(make([]byte, domain.MaxContractCodeBytes))
			Expect(domain.ValidateContractOp(op)).To(Succeed())
		})

		DescribeTable("rejects",
			func(mutate func(op *domain.ContractOp), base func() *domain.ContractOp) {
				op := base()
				mutate(op)
				Expect(domain.ValidateContractOp(op)).To(MatchError(domain.ErrInvalidContractOp))
			},
			Entry("unknown kind", func(op *domain.ContractOp) { op.Kind = "upgrade" }, validDeploy),
			Entry("empty kind", func(op *domain.ContractOp) { op.Kind = "" }, validCall),
			Entry("deploy with target", func(op *domain.ContractOp) { op.Target = target }, validDeploy),
			Entry("deploy without code", func(op *domain.ContractOp) { op.Code = "" }, validDeploy),
			Entry("deploy with upper-case hex", func(op *domain.ContractOp) { op.Code = "0A" }, validDeploy),
			Entry("deploy with odd-length hex", func(op *domain.ContractOp) { op.Code = "010" }, validDeploy),
			Entry("deploy with non-hex code", func(op *domain.ContractOp) { op.Code = "zz" }, validDeploy),
			Entry("deploy with code above MaxContractCodeBytes", func(op *domain.ContractOp) {
				op.Code = hex.EncodeToString(make([]byte, domain.MaxContractCodeBytes+1))
			}, validDeploy),
			Entry("deploy with zero gas", func(op *domain.ContractOp) { op.GasLimit = 0 }, validDeploy),
			Entry("deploy with gas above MaxContractGas", func(op *domain.ContractOp) { op.GasLimit = domain.MaxContractGas + 1 }, validDeploy),
			Entry("call with code", func(op *domain.ContractOp) { op.Code = "01" }, validCall),
			Entry("call without target", func(op *domain.ContractOp) { op.Target = "" }, validCall),
			Entry("call with short target", func(op *domain.ContractOp) { op.Target = target[:62] }, validCall),
			Entry("call with long target", func(op *domain.ContractOp) { op.Target = target + "ab" }, validCall),
			Entry("call with upper-case target", func(op *domain.ContractOp) { op.Target = strings.ToUpper(target) }, validCall),
			Entry("call with non-hex target", func(op *domain.ContractOp) { op.Target = strings.Repeat("zz", 32) }, validCall),
			Entry("call with zero gas", func(op *domain.ContractOp) { op.GasLimit = 0 }, validCall),
			Entry("call with gas above MaxContractGas", func(op *domain.ContractOp) { op.GasLimit = domain.MaxContractGas + 1 }, validCall),
		)
	})
})
