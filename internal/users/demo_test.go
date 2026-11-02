package users

import (
	"context"
	"testing"

	"golang.org/x/crypto/bcrypt"
)

func TestExplicitDemoBootstrapAndLogin(t *testing.T) {
	repo := newMemory()
	s, err := New(repo, Config{Origin: "https://example.test", BcryptCost: bcrypt.MinCost})
	if err != nil {
		t.Fatal(err)
	}
	if err = s.Bootstrap(context.Background(), "admin", "admin123"); err != nil {
		t.Fatal(err)
	}
	loginAs(t, s, "admin", "admin123")
	if validPassword("admin123") {
		t.Fatal("demo exception must not relax normal password policy")
	}
	if err = s.Bootstrap(context.Background(), "other", "admin123"); err == nil {
		t.Fatal("short bootstrap password accepted for other user")
	}
}
