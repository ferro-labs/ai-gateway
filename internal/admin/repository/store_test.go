package repository

import "testing"

func TestKeyStoreImplementsStore(_ *testing.T) {
	var _ Store = (*KeyStore)(nil)
}
