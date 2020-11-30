package epub

import (
	"testing"
)

func TestOpenReader(t *testing.T) {
	if _, err := OpenReader("/etc/fstab"); err == nil {
		t.Errorf("OpenReader() = no error")
	}
}
