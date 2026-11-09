package app

import (
	"context"
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/jackc/pgx/v5"
)

// Duraciones de SIMPLE_JWT (SIMPLE_JWT = SIMPLE_JWT_SHORT en settings.py).
const (
	accessTokenLifetime  = 15 * time.Minute
	refreshTokenLifetime = 24 * time.Hour

	cookieAccessName  = "access_token"
	cookieRefreshName = "refresh_token"
)

type User struct {
	ID       int64
	Name     string
	Username string
	Email    string
	TipoID   *int64
	TipoDesc *string
	IsActive bool
}

const userSelect = `SELECT u.id, u.name, u.username, u.email, u.tipo_id, t.description, u.is_active
	FROM usuarios_usuarios u LEFT JOIN usuarios_tipousuario t ON t.id = u.tipo_id`

func scanUser(row pgx.Row) (*User, error) {
	u := &User{}
	if err := row.Scan(&u.ID, &u.Name, &u.Username, &u.Email, &u.TipoID, &u.TipoDesc, &u.IsActive); err != nil {
		return nil, err
	}
	return u, nil
}

func (a *App) userByID(ctx context.Context, id int64) (*User, error) {
	return scanUser(a.db.QueryRow(ctx, userSelect+` WHERE u.id = $1`, id))
}

// ---------- Autenticación (rest_framework_simplejwt.authentication.JWTAuthentication) ----------

type authFailure struct{ body OM }

func tokenNotValid(message string) *authFailure {
	return &authFailure{body: obj(
		"detail", "Given token not valid for any token type",
		"code", "token_not_valid",
		"messages", []OM{obj("token_class", "AccessToken", "token_type", "access", "message", message)},
	)}
}

func (a *App) authenticate(r *http.Request) (*User, *authFailure) {
	header := r.Header.Get("Authorization")
	if header == "" {
		return nil, nil
	}
	parts := strings.Fields(header)
	if len(parts) == 0 || parts[0] != "Bearer" {
		return nil, nil
	}
	if len(parts) != 2 {
		return nil, &authFailure{body: obj(
			"detail", "Authorization header must contain two space-delimited values",
			"code", "bad_authorization_header")}
	}

	claims, msg := a.verifyToken(r.Context(), parts[1], "access")
	if msg != "" {
		return nil, tokenNotValid(msg)
	}
	rawID, ok := claims["user_id"]
	if !ok {
		return nil, &authFailure{body: obj("detail", "Token contained no recognizable user identification", "code", "token_not_valid")}
	}
	userID, ok := pyInt(rawID)
	notFound := &authFailure{body: obj("detail", "User not found", "code", "user_not_found")}
	if !ok {
		return nil, notFound
	}
	u, err := a.userByID(r.Context(), userID)
	if err != nil {
		return nil, notFound
	}
	if !u.IsActive {
		return nil, &authFailure{body: obj("detail", "User is inactive", "code", "user_inactive")}
	}
	return u, nil
}

// verifyToken replica Token.__init__ de simplejwt: firma HS256, exp, tipo, jti y,
// para refresh, la blacklist. Devuelve el mensaje de error de simplejwt si falla.
func (a *App) verifyToken(ctx context.Context, raw, wantType string) (jwt.MapClaims, string) {
	claims := jwt.MapClaims{}
	_, err := jwt.ParseWithClaims(raw, claims, func(*jwt.Token) (any, error) {
		return []byte(a.cfg.SecretKey), nil
	}, jwt.WithValidMethods([]string{"HS256"}), jwt.WithIssuedAt(), jwt.WithJSONNumber(), jwt.WithExpirationRequired())
	if err != nil {
		return nil, "Token is invalid or expired"
	}
	if t, _ := claims["token_type"].(string); t != wantType {
		return nil, "Token has wrong type"
	}
	jti, ok := claims["jti"].(string)
	if !ok {
		return nil, "Token has no id"
	}
	if wantType == "refresh" {
		var black bool
		err := a.db.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM token_blacklist_blacklistedtoken b
			JOIN token_blacklist_outstandingtoken o ON o.id = b.token_id WHERE o.jti = $1)`, jti).Scan(&black)
		if err != nil {
			return nil, "Token is invalid or expired"
		}
		if black {
			return nil, "Token is blacklisted"
		}
	}
	return claims, ""
}

func newJTI() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	// uuid4().hex
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return hex.EncodeToString(b)
}

func (a *App) sign(claims jwt.MapClaims) string {
	s, err := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString([]byte(a.cfg.SecretKey))
	if err != nil {
		panic(err)
	}
	return s
}

type refreshToken struct {
	raw     string
	jti     string
	userID  any
	iat     any
	created time.Time
	expires time.Time
}

// newRefreshToken replica RefreshToken.for_user(user) y lo registra en
// token_blacklist_outstandingtoken como hace la app token_blacklist.
// Siempre dura REFRESH_TOKEN_LIFETIME: en Django el string del token se generaba
// antes del set_exp de "recuérdame", así que esa duración nunca llegaba a la cookie.
func (a *App) newRefreshToken(ctx context.Context, userID int64) (*refreshToken, error) {
	now := time.Now().UTC()
	t := &refreshToken{jti: newJTI(), userID: userID, iat: now.Unix(), created: now, expires: now.Add(refreshTokenLifetime)}
	t.raw = a.sign(jwt.MapClaims{
		"token_type": "refresh",
		"exp":        t.expires.Unix(),
		"iat":        now.Unix(),
		"jti":        t.jti,
		"user_id":    userID,
	})
	_, err := a.db.Exec(ctx, `INSERT INTO token_blacklist_outstandingtoken (token, created_at, expires_at, user_id, jti)
		VALUES ($1, $2, $3, $4, $5)`, t.raw, now, time.Unix(t.expires.Unix(), 0).UTC(), userID, t.jti)
	return t, err
}

// accessFrom replica refresh.access_token: copia los claims del refresh (salvo
// token_type, exp y jti) y vence ACCESS_TOKEN_LIFETIME después.
func (a *App) accessFrom(userID, iat any, from time.Time) string {
	return a.sign(jwt.MapClaims{
		"token_type": "access",
		"exp":        from.Add(accessTokenLifetime).Unix(),
		"iat":        iat,
		"jti":        newJTI(),
		"user_id":    userID,
	})
}

// blacklist replica RefreshToken.blacklist().
func (a *App) blacklist(ctx context.Context, raw string, claims jwt.MapClaims) error {
	jti, _ := claims["jti"].(string)
	exp, _ := pyInt(claims["exp"])
	tx, err := a.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var id int64
	err = tx.QueryRow(ctx, `SELECT id FROM token_blacklist_outstandingtoken WHERE jti = $1`, jti).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		err = tx.QueryRow(ctx, `INSERT INTO token_blacklist_outstandingtoken (token, created_at, expires_at, user_id, jti)
			VALUES ($1, NULL, $2, NULL, $3) RETURNING id`, raw, time.Unix(exp, 0).UTC(), jti).Scan(&id)
	}
	if err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO token_blacklist_blacklistedtoken (blacklisted_at, token_id)
		SELECT $1, $2 WHERE NOT EXISTS (SELECT 1 FROM token_blacklist_blacklistedtoken WHERE token_id = $2)`, nowUTC(), id); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// ---------- Cookies (HttpResponse.set_cookie / delete_cookie de Django) ----------

func (c *Ctx) setCookie(name, value string, maxAge int) {
	http.SetCookie(c.w, &http.Cookie{
		Name:     name,
		Value:    value,
		MaxAge:   maxAge,
		Expires:  time.Now().UTC().Add(time.Duration(maxAge) * time.Second),
		HttpOnly: true,
		Secure:   c.a.cfg.JWTCookieSecure,
		SameSite: http.SameSiteNoneMode,
		Path:     "/",
		Domain:   c.a.cfg.JWTCookieDomain,
	})
}

func (c *Ctx) deleteCookie(name string) {
	// Django pone Secure cuando SameSite=None
	http.SetCookie(c.w, &http.Cookie{
		Name:     name,
		Value:    "",
		MaxAge:   -1,
		Expires:  time.Unix(0, 0).UTC(),
		Secure:   true,
		SameSite: http.SameSiteNoneMode,
		Path:     "/",
		Domain:   c.a.cfg.JWTCookieDomain,
	})
}

// ---------- Contraseñas (PBKDF2PasswordHasher de Django 3.2) ----------

const pbkdf2Iterations = 260000

const saltChars = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"

func makePassword(password string) string {
	salt := make([]byte, 22)
	rnd := make([]byte, 22)
	_, _ = rand.Read(rnd)
	for i := range salt {
		// 62 caracteres: el sesgo de módulo es despreciable para una sal
		salt[i] = saltChars[int(rnd[i])%len(saltChars)]
	}
	key, err := pbkdf2.Key(sha256.New, password, salt, pbkdf2Iterations, 32)
	if err != nil {
		panic(err)
	}
	return fmt.Sprintf("pbkdf2_sha256$%d$%s$%s", pbkdf2Iterations, salt, base64.StdEncoding.EncodeToString(key))
}

// dummyPasswordHash se usa cuando el usuario no existe, para que la respuesta tarde lo mismo.
var dummyPasswordHash = makePassword("sphere-dummy-password")

// checkPassword verifica una contraseña contra un hash "pbkdf2_sha256$iter$salt$hash" de Django.
// Un hash vacío o inutilizable (p. ej. "!...") nunca coincide.
func checkPassword(password, encoded string) bool {
	parts := strings.SplitN(encoded, "$", 4)
	if len(parts) != 4 || parts[0] != "pbkdf2_sha256" {
		return false
	}
	iterations, err := strconv.Atoi(parts[1])
	if err != nil || iterations <= 0 {
		return false
	}
	expected, err := base64.StdEncoding.DecodeString(parts[3])
	if err != nil || len(expected) == 0 {
		return false
	}
	key, err := pbkdf2.Key(sha256.New, password, []byte(parts[2]), iterations, len(expected))
	if err != nil {
		return false
	}
	return subtle.ConstantTimeCompare(key, expected) == 1
}

// jsonInt64 normaliza un claim numérico (json.Number) para reutilizarlo al firmar.
func jsonInt64(v any) any {
	if n, ok := v.(json.Number); ok {
		if i, err := n.Int64(); err == nil {
			return i
		}
		if f, err := strconv.ParseFloat(n.String(), 64); err == nil {
			return f
		}
	}
	return v
}
