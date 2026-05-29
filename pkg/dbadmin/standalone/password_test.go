package standalone

import "testing"

// fastPolicy uses lower Argon2 cost so the test suite stays fast.
func fastPolicy() PasswordPolicy {
	return PasswordPolicy{
		Time:    1,
		Memory:  8 * 1024,
		Threads: 1,
		KeyLen:  32,
		SaltLen: 16,
		MinLen:  14,
		Version: 1,
	}
}

func TestHashVerifyRoundTrip(t *testing.T) {
	p := fastPolicy()
	enc, err := HashPassword("correct-horse-battery", p)
	if err != nil {
		t.Fatalf("hash: %v", err)
	}
	ok, rehash, err := VerifyPassword("correct-horse-battery", enc, p)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if !ok {
		t.Fatal("expected verify success")
	}
	if rehash {
		t.Fatal("did not expect rehash for identical params")
	}
}

func TestVerifyRejectsBadPassword(t *testing.T) {
	p := fastPolicy()
	enc, _ := HashPassword("correct-horse-battery", p)
	ok, _, err := VerifyPassword("wrong-password!!!", enc, p)
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Fatal("expected verify failure")
	}
}

func TestVerifyDetectsRehashNeeded(t *testing.T) {
	weak := fastPolicy()
	weak.Time = 1
	weak.Memory = 8 * 1024
	enc, _ := HashPassword("correct-horse-battery", weak)
	stronger := weak
	stronger.Memory = 16 * 1024
	ok, rehash, err := VerifyPassword("correct-horse-battery", enc, stronger)
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("expected verify success")
	}
	if !rehash {
		t.Fatal("expected rehash flag when params drift")
	}
}

func TestHashRefusesShort(t *testing.T) {
	if _, err := HashPassword("short", fastPolicy()); err == nil {
		t.Fatal("expected error for short password")
	}
}

func TestVerifyRejectsMalformed(t *testing.T) {
	_, _, err := VerifyPassword("password", "not-a-phc-hash", fastPolicy())
	if err == nil {
		t.Fatal("expected error for malformed PHC hash")
	}
}
