package server

import (
	"net/http"
	"net/http/httptest"
	"testing/fstest"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// untouchedChain panics on any call: explorer requests must never reach the Chain.
type untouchedChain struct{ Chain }

var _ = Describe("Explorer", func() {
	const csp = "default-src 'self'; script-src 'self'; style-src 'self'; connect-src 'self'; img-src 'self'; base-uri 'none'; frame-ancestors 'none'"

	files := fstest.MapFS{
		"index.html": {Data: []byte("<!doctype html><title>x</title>")},
		"app.js":     {Data: []byte("// js")},
		"style.css":  {Data: []byte("body{}")},
	}

	serve := func(cfg Config, method, target string) *httptest.ResponseRecorder {
		srv, err := NewServer(untouchedChain{}, cfg)
		Expect(err).NotTo(HaveOccurred())

		rec := httptest.NewRecorder()
		srv.Handler().ServeHTTP(rec, httptest.NewRequest(method, target, nil))

		return rec
	}

	It("GET /explorer/ is 404 when Config.Explorer is nil", func() {
		Expect(serve(Config{}, http.MethodGet, "/explorer/").Code).To(Equal(http.StatusNotFound))
	})

	It("serves index.html at /explorer/", func() {
		rec := serve(Config{Explorer: files}, http.MethodGet, "/explorer/")

		Expect(rec.Code).To(Equal(http.StatusOK))
		Expect(rec.Header().Get("Content-Type")).To(Equal("text/html; charset=utf-8"))
		Expect(rec.Body.String()).To(ContainSubstring("<title>x</title>"))
	})

	It("serves app.js with a JavaScript type", func() {
		rec := serve(Config{Explorer: files}, http.MethodGet, "/explorer/app.js")

		Expect(rec.Code).To(Equal(http.StatusOK))
		Expect(rec.Header().Get("Content-Type")).To(ContainSubstring("javascript"))
		Expect(rec.Body.String()).To(Equal("// js"))
	})

	It("serves style.css", func() {
		rec := serve(Config{Explorer: files}, http.MethodGet, "/explorer/style.css")

		Expect(rec.Code).To(Equal(http.StatusOK))
		Expect(rec.Header().Get("Content-Type")).To(ContainSubstring("text/css"))
	})

	It("answers HEAD without a body", func() {
		rec := serve(Config{Explorer: files}, http.MethodHead, "/explorer/")

		Expect(rec.Code).To(Equal(http.StatusOK))
		Expect(rec.Body.Len()).To(BeZero())
	})

	It("sets the Content-Security-Policy on explorer responses", func() {
		for _, target := range []string{"/explorer/", "/explorer/app.js", "/explorer/missing.js"} {
			Expect(serve(Config{Explorer: files}, http.MethodGet, target).Header().Get("Content-Security-Policy")).To(Equal(csp), target)
		}
	})

	It("sets X-Content-Type-Options nosniff", func() {
		for _, target := range []string{"/explorer/", "/explorer/app.js", "/explorer/missing.js"} {
			Expect(serve(Config{Explorer: files}, http.MethodGet, target).Header().Get("X-Content-Type-Options")).To(Equal("nosniff"), target)
		}
	})

	It("answers 404 for a missing asset", func() {
		Expect(serve(Config{Explorer: files}, http.MethodGet, "/explorer/missing.js").Code).To(Equal(http.StatusNotFound))
	})

	It("does not list directories", func() {
		rec := serve(Config{Explorer: fstest.MapFS{"sub/a.js": {Data: []byte("x")}}}, http.MethodGet, "/explorer/sub/")

		Expect(rec.Code).To(Equal(http.StatusNotFound))
	})

	It("POST /explorer/ answers 405 with Allow GET, HEAD", func() {
		for _, target := range []string{"/explorer/", "/explorer/app.js"} {
			rec := serve(Config{Explorer: files}, http.MethodPost, target)

			Expect(rec.Code).To(Equal(http.StatusMethodNotAllowed), target)
			Expect(rec.Header().Get("Allow")).To(Equal("GET, HEAD"), target)
		}
	})
})
