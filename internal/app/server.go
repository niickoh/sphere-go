package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"mime"
	"net/http"
	"net/url"
	"runtime/debug"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

type App struct {
	cfg  Config
	db   *pgxpool.Pool
	http *http.Client

	routes []*route
	schema *schemaInfo
}

func New(ctx context.Context, cfg Config) (*App, error) {
	pool, err := openDB(ctx, cfg)
	if err != nil {
		return nil, err
	}
	a := &App{
		cfg:  cfg,
		db:   pool,
		http: &http.Client{Timeout: 30 * time.Second},
	}
	if a.schema, err = loadSchemaInfo(ctx, pool); err != nil {
		pool.Close()
		return nil, err
	}
	if err := loadTemplates(); err != nil {
		pool.Close()
		return nil, err
	}
	a.registerRoutes()
	return a, nil
}

func (a *App) Close() { a.db.Close() }

// ---------- Rutas ----------

// Perm replica las permission_classes de DRF usadas en el proyecto.
type Perm int

const (
	AllowAny Perm = iota
	IsAuthenticated
	IsClubAdmin // usuarios.permissions.IsClubAdmin
	IsClubStaff // usuarios.permissions.IsClubStaff
)

const (
	tipoAdmin      = 1
	tipoEntrenador = 2
)

type segment struct {
	lit   string
	param string
	isInt bool
}

type route struct {
	pattern string
	segs    []segment
	methods []string
	perm    Perm
	h       func(*Ctx)
}

// handle registra una ruta con la sintaxis de Django: "usuarios/editar/<int:id>".
func (a *App) handle(pattern string, methods []string, perm Perm, h func(*Ctx)) {
	r := &route{pattern: pattern, methods: methods, perm: perm, h: h}
	for _, s := range strings.Split(pattern, "/") {
		if strings.HasPrefix(s, "<") && strings.HasSuffix(s, ">") {
			conv, name, _ := strings.Cut(s[1:len(s)-1], ":")
			r.segs = append(r.segs, segment{param: name, isInt: conv == "int"})
		} else {
			r.segs = append(r.segs, segment{lit: s})
		}
	}
	a.routes = append(a.routes, r)
}

func (r *route) match(path string) (map[string]string, bool) {
	parts := strings.Split(path, "/")
	if len(parts) != len(r.segs) {
		return nil, false
	}
	var params map[string]string
	for i, s := range r.segs {
		p := parts[i]
		if s.param == "" {
			if p != s.lit {
				return nil, false
			}
			continue
		}
		if p == "" || (s.isInt && strings.Trim(p, "0123456789") != "") {
			return nil, false
		}
		if params == nil {
			params = map[string]string{}
		}
		params[s.param] = p
	}
	return params, true
}

func (a *App) find(path string) (*route, map[string]string) {
	for _, r := range a.routes {
		if p, ok := r.match(path); ok {
			return r, p
		}
	}
	return nil, nil
}

// allowed devuelve el header Allow que pone DRF (los métodos de la vista más OPTIONS).
func (r *route) allowed() []string {
	return append(slices.Clone(r.methods), "OPTIONS")
}

// ---------- Handler principal ----------

func (a *App) Handler() http.Handler { return http.HandlerFunc(a.serve) }

func (a *App) serve(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	rec := &statusRecorder{ResponseWriter: w, status: 200}
	defer func() {
		log.Printf("%s %s %d %s", r.Method, r.URL.RequestURI(), rec.status, time.Since(start).Round(time.Millisecond))
	}()
	w = rec

	h := w.Header()
	// SecurityMiddleware y XFrameOptionsMiddleware de Django
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Referrer-Policy", "same-origin")
	h.Set("X-Frame-Options", "DENY")

	// django-cors-headers
	origin := r.Header.Get("Origin")
	h.Add("Vary", "origin")
	if origin != "" && slices.Contains(a.cfg.CORSAllowedOrigins, origin) {
		h.Set("Access-Control-Allow-Origin", origin)
		h.Set("Access-Control-Allow-Credentials", "true")
		if r.Method == http.MethodOptions {
			h.Set("Access-Control-Allow-Headers", strings.Join(a.cfg.CORSAllowHeaders, ", "))
			h.Set("Access-Control-Allow-Methods", strings.Join(a.cfg.CORSAllowMethods, ", "))
			h.Set("Access-Control-Max-Age", "86400")
		}
	}
	if r.Method == http.MethodOptions && r.Header.Get("Access-Control-Request-Method") != "" {
		h.Set("Content-Type", "text/html; charset=utf-8")
		h.Set("Content-Length", "0")
		w.WriteHeader(http.StatusOK)
		return
	}

	path := strings.TrimPrefix(r.URL.Path, "/")
	if a.serveDocs(w, r, path) {
		return
	}
	rt, params := a.find(path)
	if rt == nil {
		// APPEND_SLASH de CommonMiddleware
		if !strings.HasSuffix(path, "/") {
			if rt2, _ := a.find(path + "/"); rt2 != nil {
				loc := r.URL.Path + "/"
				if r.URL.RawQuery != "" {
					loc += "?" + r.URL.RawQuery
				}
				http.Redirect(w, r, loc, http.StatusMovedPermanently)
				return
			}
		}
		notFound(w)
		return
	}

	// usuarios.middleware.JWTAuthCookieMiddleware: la cookie manda sobre el header
	if c, err := r.Cookie("access_token"); err == nil && c.Value != "" {
		r.Header.Set("Authorization", "Bearer "+c.Value)
	}

	c := &Ctx{a: a, w: w, r: r, params: params, route: rt}
	h.Set("Allow", strings.Join(rt.allowed(), ", "))
	h.Add("Vary", "Accept")

	defer func() {
		if p := recover(); p != nil {
			log.Printf("panic en %s %s: %v\n%s", r.Method, r.URL.Path, p, debug.Stack())
			serverErrorPage(w)
		}
	}()

	// DRF autentica antes de evaluar permisos y método
	user, fail := a.authenticate(r)
	if fail != nil {
		c.unauthorized(fail)
		return
	}
	c.user = user
	if !c.checkPerm(rt.perm) {
		return
	}

	if r.Method == http.MethodOptions {
		c.JSON(200, obj("name", rt.pattern, "renders", []string{"application/json"},
			"parses", []string{"application/json", "application/x-www-form-urlencoded", "multipart/form-data"}))
		return
	}
	if !slices.Contains(rt.methods, r.Method) {
		c.JSON(405, obj("detail", fmt.Sprintf("Method \"%s\" not allowed.", r.Method)))
		return
	}
	rt.h(c)
}

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (s *statusRecorder) WriteHeader(code int) {
	s.status = code
	s.ResponseWriter.WriteHeader(code)
}

func notFound(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "text/html")
	w.WriteHeader(http.StatusNotFound)
	_, _ = io.WriteString(w, "<!doctype html>\n<html lang=\"en\">\n<head>\n  <title>Not Found</title>\n</head>\n<body>\n  <h1>Not Found</h1><p>The requested resource was not found on this server.</p>\n</body>\n</html>\n")
}

func serverErrorPage(w http.ResponseWriter) {
	// La página de error de Django no pasa por DRF, así que no lleva Allow
	w.Header().Del("Allow")
	w.Header().Set("Content-Type", "text/html")
	w.WriteHeader(http.StatusInternalServerError)
	_, _ = io.WriteString(w, "<!doctype html>\n<html lang=\"en\">\n<head>\n  <title>Server Error (500)</title>\n</head>\n<body>\n  <h1>Server Error (500)</h1><p></p>\n</body>\n</html>\n")
}

// ---------- Contexto de la petición ----------

type Ctx struct {
	a      *App
	w      http.ResponseWriter
	r      *http.Request
	user   *User
	params map[string]string
	route  *route

	body     []byte
	bodyRead bool
}

func (c *Ctx) ctx() context.Context { return c.r.Context() }

func (c *Ctx) JSON(status int, v any) {
	b, err := marshalJSON(v)
	if err != nil {
		c.serverError(err)
		return
	}
	c.w.Header().Set("Content-Type", "application/json")
	c.w.WriteHeader(status)
	_, _ = c.w.Write(b)
}

// serverError equivale a una excepción no controlada en la vista de Django (500 HTML).
func (c *Ctx) serverError(err error) {
	log.Printf("error en %s %s: %v", c.r.Method, c.r.URL.Path, err)
	serverErrorPage(c.w)
}

func (c *Ctx) intParam(name string) int64 {
	n, err := strconv.ParseInt(c.params[name], 10, 64)
	if err != nil {
		// Un <int:...> que no cabe en int64 se trata como inexistente
		panic(fmt.Errorf("parámetro %s fuera de rango", name))
	}
	return n
}

func (c *Ctx) rawBody() []byte {
	if !c.bodyRead {
		c.bodyRead = true
		c.body, _ = io.ReadAll(io.LimitReader(c.r.Body, 10<<20))
	}
	return c.body
}

var errNotObject = errors.New("el body no es un objeto JSON")

// loads replica json.loads(request.body) (números se conservan tal cual).
func (c *Ctx) loads() (any, error) {
	return decodeJSON(c.rawBody())
}

// loadsObj replica body = json.loads(request.body) seguido de body.get(...).
// Devuelve errNotObject si el JSON es válido pero no es un objeto (en Python: AttributeError -> 500).
func (c *Ctx) loadsObj() (map[string]any, error) {
	v, err := c.loads()
	if err != nil {
		return nil, err
	}
	m, ok := v.(map[string]any)
	if !ok {
		return nil, errNotObject
	}
	return m, nil
}

func decodeJSON(b []byte) (any, error) {
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return nil, err
	}
	if dec.More() {
		return nil, errors.New("Extra data")
	}
	if _, err := dec.Token(); err != io.EOF {
		return nil, errors.New("Extra data")
	}
	return v, nil
}

// data replica request.data de DRF (JSON, form-urlencoded o multipart).
// Si falla escribe la respuesta de error de DRF y devuelve ok=false.
func (c *Ctx) data() (map[string]any, bool) {
	body := c.rawBody()
	ct := c.r.Header.Get("Content-Type")
	if len(body) == 0 && ct == "" {
		return map[string]any{}, true
	}
	mt, _, _ := mime.ParseMediaType(ct)
	switch {
	case mt == "application/json" || strings.HasSuffix(mt, "+json"):
		if len(body) == 0 {
			return map[string]any{}, true
		}
		v, err := decodeJSON(body)
		if err != nil {
			c.JSON(400, obj("detail", "JSON parse error - "+err.Error()))
			return nil, false
		}
		m, ok := v.(map[string]any)
		if !ok {
			// DRF devolvería la lista/escalar y la vista fallaría al hacer .get()
			panic(errNotObject)
		}
		return m, true
	case mt == "application/x-www-form-urlencoded":
		vals, err := url.ParseQuery(string(body))
		if err != nil {
			c.JSON(400, obj("detail", "Form parse error"))
			return nil, false
		}
		return formToMap(vals), true
	case mt == "multipart/form-data":
		c.r.Body = io.NopCloser(bytes.NewReader(body))
		if err := c.r.ParseMultipartForm(10 << 20); err != nil {
			c.JSON(400, obj("detail", "Multipart form parse error - "+err.Error()))
			return nil, false
		}
		return formToMap(c.r.MultipartForm.Value), true
	case len(body) == 0:
		return map[string]any{}, true
	default:
		c.JSON(415, obj("detail", fmt.Sprintf("Unsupported media type \"%s\" in request.", ct)))
		return nil, false
	}
}

// QueryDict.get() devuelve el último valor de cada clave.
func formToMap(v map[string][]string) map[string]any {
	m := make(map[string]any, len(v))
	for k, vs := range v {
		if len(vs) > 0 {
			m[k] = vs[len(vs)-1]
		}
	}
	return m
}

// ---------- Permisos ----------

func (u *User) tipo() int64 {
	if u == nil || u.TipoID == nil {
		return 0
	}
	return *u.TipoID
}

func isAdmin(u *User) bool { return u != nil && u.tipo() == tipoAdmin }

func isStaffMember(u *User) bool {
	t := u.tipo()
	return u != nil && (t == tipoAdmin || t == tipoEntrenador)
}

func isSelfOrStaff(u *User, userID int64) bool {
	return u != nil && (u.ID == userID || isStaffMember(u))
}

func (c *Ctx) checkPerm(p Perm) bool {
	ok := true
	switch p {
	case IsAuthenticated:
		ok = c.user != nil
	case IsClubAdmin:
		ok = isAdmin(c.user)
	case IsClubStaff:
		ok = isStaffMember(c.user)
	}
	if ok {
		return true
	}
	if c.user == nil {
		c.unauthorized(&authFailure{body: obj("detail", "Authentication credentials were not provided.")})
		return false
	}
	c.JSON(403, obj("detail", "No tienes permisos para realizar esta acción"))
	return false
}

func (c *Ctx) unauthorized(f *authFailure) {
	c.w.Header().Set("WWW-Authenticate", `Bearer realm="api"`)
	c.JSON(401, f.body)
}
