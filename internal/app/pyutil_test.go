package app

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"
)

func TestCountMonthlyOccurrences(t *testing.T) {
	cases := []struct {
		start, end any
		want       int
	}{
		{"2026-01-31", "2026-12-31T00:00:00Z", 11},
		{"2026-01-01T00:00:00", "2026-04-30", 3},
		{"2026-03-15T10:00:00+03:00", "2026-12-31T00:00:00Z", 9},
		{"2026-05-01", "2026-01-01", 0},
		{"no-es-fecha", "2026-01-01", 0},
	}
	for _, c := range cases {
		got, err := countMonthlyOccurrences(c.start, c.end)
		if err != nil || got != c.want {
			t.Errorf("countMonthlyOccurrences(%v, %v) = %d, %v; want %d", c.start, c.end, got, err, c.want)
		}
	}
	if _, err := countMonthlyOccurrences(nil, "2026-01-01"); err == nil || err.Error() != "'NoneType' object has no attribute 'tzinfo'" {
		t.Errorf("error inesperado: %v", err)
	}
}

func TestPyHelpers(t *testing.T) {
	if got := pyRepr("Invalid status '0'"); got != `"Invalid status '0'"` {
		t.Errorf("pyRepr = %s", got)
	}
	if got := pyRepr("abc"); got != "'abc'" {
		t.Errorf("pyRepr = %s", got)
	}
	if n, ok := pyInt(json.Number("2.9")); !ok || n != 2 {
		t.Errorf("pyInt(2.9) = %d %v", n, ok)
	}
	if _, ok := intKey("5"); ok {
		t.Error(`intKey("5") debe fallar como en un dict de Python`)
	}
	ts := time.Date(2026, 1, 5, 8, 0, 0, 654321000, time.UTC)
	if got := drfISO(ts); got != "2026-01-05T08:00:00.654321Z" {
		t.Errorf("drfISO = %s", got)
	}
	if got := pyISO(ts.Truncate(time.Second)); got != "2026-01-05T08:00:00+00:00" {
		t.Errorf("pyISO = %s", got)
	}
	if got := djangoDateTime(time.Date(2026, 10, 12, 15, 4, 0, 0, time.UTC)); got != "Oct. 12, 2026, 3:04 p.m." {
		t.Errorf("djangoDateTime = %s", got)
	}
	if got := djangoDateTime(time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)); got != "Sept. 1, 2026, noon" {
		t.Errorf("djangoDateTime = %s", got)
	}
}

func TestValidateEmail(t *testing.T) {
	for _, ok := range []string{"a@b.cl", "nombre.apellido+x@sub.dominio.com", "x@localhost"} {
		if !validateEmail(ok) {
			t.Errorf("%s debería ser válido", ok)
		}
	}
	for _, bad := range []string{"", "no-es-email", "a@b", "a@b.c-", "a b@c.cl", "@b.cl"} {
		if validateEmail(bad) {
			t.Errorf("%s debería ser inválido", bad)
		}
	}
}

// Escribe un hash para verificarlo con check_password de Django (ver README).
func TestMakePasswordFormat(t *testing.T) {
	h := makePassword("secreto")
	parts := strings.Split(h, "$")
	if len(parts) != 4 || parts[0] != "pbkdf2_sha256" || parts[1] != "260000" || len(parts[2]) != 22 {
		t.Fatalf("formato inesperado: %s", h)
	}
	if p := os.Getenv("HASH_OUT"); p != "" {
		_ = os.WriteFile(p, []byte(h), 0o600)
	}
}
