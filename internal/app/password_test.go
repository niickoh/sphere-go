package app

import "testing"

func TestCheckPassword(t *testing.T) {
	hash := makePassword("secreto123")

	if !checkPassword("secreto123", hash) {
		t.Fatal("la contraseña correcta no coincide con su hash")
	}
	if checkPassword("otra", hash) {
		t.Fatal("una contraseña incorrecta coincide")
	}
	for _, bad := range []string{"", "!unusable", "md5$x$y", "pbkdf2_sha256$abc$salt$hash", "pbkdf2_sha256$1$salt$%%%"} {
		if checkPassword("secreto123", bad) {
			t.Fatalf("un hash inválido coincide: %q", bad)
		}
	}
}

// Hash en formato Django calculado con otra implementación (Node crypto.pbkdf2Sync),
// equivalente a make_password("admin", salt="abcdefghijklmnopqrstuv") en Django 3.2.
func TestCheckPasswordDjangoHash(t *testing.T) {
	const django = "pbkdf2_sha256$260000$abcdefghijklmnopqrstuv$WKFFvuzjaaihdGHAMz50ugqOYZFymuZGDkz9MNLqYfw="
	if !checkPassword("admin", django) {
		t.Fatal("no coincide con un hash en formato Django")
	}
	if checkPassword("Admin", django) {
		t.Fatal("coincide con una contraseña distinta")
	}
}
