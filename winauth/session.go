// Package winauth exposes platform authentication sessions without exporting
// credentials or authentication tokens to preferences or task journals.
package winauth

type Session interface {
	Next(input []byte) (output []byte, complete bool, err error)
	Package() (string, error)
	Close()
}
