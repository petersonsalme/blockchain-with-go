package bolt_test

import (
	. "github.com/onsi/gomega"
	bbolt "go.etcd.io/bbolt"
)

// dumpBuckets returns every key/value of the named buckets, byte for byte, in key order.
func dumpBuckets(path string, names ...string) map[string][][2]string {
	raw, err := bbolt.Open(path, 0o600, &bbolt.Options{ReadOnly: true})

	Expect(err).NotTo(HaveOccurred())
	defer func() { Expect(raw.Close()).To(Succeed()) }()

	out := map[string][][2]string{}

	Expect(raw.View(func(tx *bbolt.Tx) error {
		for _, name := range names {
			b := tx.Bucket([]byte(name))
			Expect(b).NotTo(BeNil(), name)
			out[name] = [][2]string{}

			Expect(b.ForEach(func(k, v []byte) error {
				out[name] = append(out[name], [2]string{string(k), string(v)})
				return nil
			})).To(Succeed())
		}

		return nil
	})).To(Succeed())

	return out
}
