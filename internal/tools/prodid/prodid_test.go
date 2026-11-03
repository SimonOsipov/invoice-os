package prodid

import "testing"

func TestSum_KnownAnswer(t *testing.T) {
	const abc = "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad"
	if got := Sum("abc"); got != abc {
		t.Errorf("Sum(abc) = %s, want %s", got, abc)
	}
}
