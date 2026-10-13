package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// ---------- usuarios.views ----------

// GET usuarios/listar/
func (a *App) listarUsuarios(c *Ctx) {
	q := `SELECT u.id, u.name, u.username, u.email, t.description, t.id
		FROM usuarios_usuarios u LEFT JOIN usuarios_tipousuario t ON t.id = u.tipo_id`
	var args []any
	if tipoQ := c.r.URL.Query().Get("tipo"); tipoQ != "" {
		if n, ok := pyInt(tipoQ); ok {
			q += ` WHERE u.tipo_id = $1`
			args = append(args, n)
		} else {
			q += ` WHERE t.description ILIKE $1 ESCAPE '\'`
			args = append(args, "%"+likeEscape(tipoQ)+"%")
		}
	}
	data := []OM{}
	rows, err := a.db.Query(c.ctx(), q, args...)
	if err != nil {
		c.serverError(err)
		return
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		var name, username, email string
		var tipo *string
		var tipoID *int64
		if err := rows.Scan(&id, &name, &username, &email, &tipo, &tipoID); err != nil {
			c.serverError(err)
			return
		}
		data = append(data, obj("id", id, "name", name, "username", username, "email", email,
			"tipo", ptrAny(tipo), "tipo_id", ptrAny(tipoID)))
	}
	if err := rows.Err(); err != nil {
		c.serverError(err)
		return
	}
	c.JSON(200, data)
}

func likeEscape(s string) string {
	r := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)
	return r.Replace(s)
}

// GET equipos/listar/
func (a *App) listarEquipos(c *Ctx) {
	rows, err := a.db.Query(c.ctx(), `SELECT e.id, e.description, e.category, e.level, e.division, e.color,
		COUNT(ut.id) FILTER (WHERE ut."idUser_id" IS NOT NULL)
		FROM usuarios_equipos e LEFT JOIN usuarios_usersteams ut ON ut."idTeam_id" = e.id
		GROUP BY e.id`)
	if err != nil {
		c.serverError(err)
		return
	}
	defer rows.Close()
	data := []OM{}
	for rows.Next() {
		var id, members int64
		var description, category, level string
		var division, color *string
		if err := rows.Scan(&id, &description, &category, &level, &division, &color, &members); err != nil {
			c.serverError(err)
			return
		}
		data = append(data, obj("id", id, "description", description, "category", category, "level", level,
			"division", ptrAny(division), "color", ptrAny(color), "members_count", members))
	}
	if err := rows.Err(); err != nil {
		c.serverError(err)
		return
	}
	c.JSON(200, data)
}

// POST usuarios/agregar/
func (a *App) addUsuario(c *Ctx) {
	body, err := c.loadsObj()
	if errors.Is(err, errNotObject) {
		c.serverError(err)
		return
	}
	if err != nil {
		c.JSON(400, obj("error", "Solicitud inválida"))
		return
	}
	name, username, email, password, tipoID := body["name"], body["username"], body["email"], body["password"], body["tipo_id"]
	if !truthy(name) || !truthy(username) || !truthy(email) || !truthy(password) || !truthy(tipoID) {
		c.JSON(400, obj("message", "Todos los campos son obligatorios"))
		return
	}

	var tipoPK int64
	var tipoDesc *string
	err = a.db.QueryRow(c.ctx(), `SELECT id, description FROM usuarios_tipousuario WHERE id = $1::text::bigint`, dbText(tipoID)).
		Scan(&tipoPK, &tipoDesc)
	if isNoRows(err) {
		c.JSON(404, obj("error", "El tipo de usuario no existe"))
		return
	}
	if err != nil {
		c.serverError(err)
		return
	}

	cols := `password, last_login, is_superuser, name, username, email, tipo_id, is_active, is_staff`
	vals := `$1, NULL, false, $2::text, $3::text, $4::text, $5, true, false`
	args := []any{makePassword(pyStr(password)), dbText(name), dbText(username), dbText(email), tipoPK}
	if a.schema.userDateJoined {
		cols += `, date_joined`
		vals += `, $6`
		args = append(args, nowUTC())
	}
	var id int64
	err = a.db.QueryRow(c.ctx(), `INSERT INTO usuarios_usuarios (`+cols+`) VALUES (`+vals+`) RETURNING id`, args...).Scan(&id)
	if err != nil {
		if isIntegrityErr(err) {
			msg := pgErrText(err)
			switch {
			case strings.Contains(msg, "email"):
				c.JSON(400, obj("error", fmt.Sprintf("El email '%s' ya está registrado", pyStr(email))))
			case strings.Contains(msg, "username"):
				c.JSON(400, obj("error", fmt.Sprintf("El username '%s' ya está registrado", pyStr(username))))
			default:
				c.JSON(400, obj("error", "Error de base de datos: Restricción de unicidad"))
			}
			return
		}
		c.JSON(500, obj("error", "Error inesperado: "+pgErrText(err)))
		return
	}
	c.JSON(201, obj(
		"message", "Usuario creado exitosamente",
		"usuario", obj("id", id, "name", name, "username", username, "email", email, "tipo", ptrAny(tipoDesc)),
	))
}

// POST equipos/add/
func (a *App) addTeam(c *Ctx) {
	body, err := c.loadsObj()
	if errors.Is(err, errNotObject) {
		c.serverError(err)
		return
	}
	if err != nil {
		c.JSON(400, obj("error", "Solicitud inválida"))
		return
	}
	description, category, level, color := body["description"], body["category"], body["level"], body["color"]
	division := body["division"]
	if !truthy(division) {
		division = nil
	}
	if !truthy(description) || !truthy(category) || !truthy(level) || !truthy(color) {
		c.JSON(400, obj("message", "Todos los campos son obligatorios"))
		return
	}
	var id int64
	err = a.db.QueryRow(c.ctx(), `INSERT INTO usuarios_equipos (description, category, level, division, color)
		VALUES ($1::text, $2::text, $3::text, $4::text, $5::text) RETURNING id`,
		dbText(description), dbText(category), dbText(level), dbText(division), dbText(color)).Scan(&id)
	if err != nil {
		c.JSON(500, obj("error", "Error inesperado: "+pgErrText(err)))
		return
	}
	c.JSON(201, obj(
		"message", "Equipo creado exitosamente",
		"equipo", obj("id", id, "description", description, "category", category, "level", level,
			"division", division, "color", color),
	))
}

// PUT usuarios/editar/<int:id>
func (a *App) editUsuario(c *Ctx) {
	id := c.intParam("id")
	if !(isAdmin(c.user) || c.user.ID == id) {
		c.JSON(403, obj("error", "No tienes permisos para editar este usuario"))
		return
	}
	u, err := a.userByID(c.ctx(), id)
	if isNoRows(err) {
		c.JSON(404, obj("error", "Usuario no encontrado"))
		return
	}
	if err != nil {
		c.serverError(err)
		return
	}
	body, err := c.loadsObj()
	if err != nil {
		c.serverError(err)
		return
	}

	var name, username, email any = u.Name, u.Username, u.Email
	sets := []string{}
	args := []any{}
	set := func(col string, v any) {
		args = append(args, v)
		sets = append(sets, fmt.Sprintf(`%s = $%d`, col, len(args)))
	}
	if v, ok := body["name"]; ok {
		name = v
		set("name", dbText(v))
	}
	if v, ok := body["username"]; ok && isAdmin(c.user) {
		username = v
		set("username", dbText(v))
	}
	if v, ok := body["email"]; ok {
		email = v
		set("email", dbText(v))
	}
	if truthy(body["password"]) {
		set("password", makePassword(pyStr(body["password"])))
	}
	tipoDesc := u.TipoDesc
	hasTipo := u.TipoID != nil
	if v, ok := body["tipo_id"]; ok && isAdmin(c.user) {
		var tid int64
		var desc *string
		err := pgx.ErrNoRows
		if v != nil {
			err = a.db.QueryRow(c.ctx(), `SELECT id, description FROM usuarios_tipousuario WHERE id = $1::text::bigint`, dbText(v)).Scan(&tid, &desc)
		}
		if isNoRows(err) {
			c.JSON(404, obj("error", "El tipo de usuario especificado no existe"))
			return
		}
		if err != nil {
			c.serverError(err)
			return
		}
		tipoDesc, hasTipo = desc, true
		set("tipo_id", tid)
	}
	if len(sets) > 0 {
		args = append(args, id)
		q := fmt.Sprintf(`UPDATE usuarios_usuarios SET %s WHERE id = $%d`, strings.Join(sets, ", "), len(args))
		if _, err := a.db.Exec(c.ctx(), q, args...); err != nil {
			c.serverError(err)
			return
		}
	}
	var tipo any
	if hasTipo {
		tipo = ptrAny(tipoDesc)
	}
	c.JSON(200, obj(
		"message", "Usuario actualizado exitosamente",
		"usuario", obj("id", u.ID, "name", name, "username", username, "email", email, "tipo", tipo),
	))
}

// PUT equipos/edit/<int:id>
func (a *App) editTeam(c *Ctx) {
	id := c.intParam("id")
	var description, category, level any
	var division, color *string
	var d, ca, l string
	err := a.db.QueryRow(c.ctx(), `SELECT description, category, level, division, color FROM usuarios_equipos WHERE id = $1`, id).
		Scan(&d, &ca, &l, &division, &color)
	if isNoRows(err) {
		c.JSON(404, obj("error", "Equipo no encontrado"))
		return
	}
	if err != nil {
		c.serverError(err)
		return
	}
	description, category, level = d, ca, l
	var div, col any = ptrAny(division), ptrAny(color)

	body, err := c.loadsObj()
	if err != nil {
		c.serverError(err)
		return
	}
	if v, ok := body["description"]; ok {
		description = v
	}
	if v, ok := body["category"]; ok {
		category = v
	}
	if v, ok := body["level"]; ok {
		level = v
	}
	if v, ok := body["division"]; ok {
		div = v
		if !truthy(v) {
			div = nil
		}
	}
	if v, ok := body["color"]; ok {
		col = v
	}
	_, err = a.db.Exec(c.ctx(), `UPDATE usuarios_equipos SET description = $1::text, category = $2::text, level = $3::text,
		division = $4::text, color = $5::text WHERE id = $6`,
		dbText(description), dbText(category), dbText(level), dbText(div), dbText(col), id)
	if err != nil {
		c.serverError(err)
		return
	}
	c.JSON(200, obj(
		"message", "Equipo actualizado exitosamente",
		"equipo", obj("id", id, "description", description, "category", category, "level", level,
			"division", div, "color", col),
	))
}

// DELETE usuarios/eliminar/<int:id>
func (a *App) deleteUsuario(c *Ctx) {
	ok, err := a.deleteByID(c.ctx(), "usuarios_usuarios", c.intParam("id"))
	if err != nil {
		c.serverError(err)
		return
	}
	if !ok {
		c.JSON(404, obj("error", "Usuario no encontrado"))
		return
	}
	c.JSON(200, obj("message", "Usuario eliminado exitosamente"))
}

// DELETE equipos/delete/<int:id>
func (a *App) deleteTeam(c *Ctx) {
	ok, err := a.deleteByID(c.ctx(), "usuarios_equipos", c.intParam("id"))
	if err != nil {
		c.serverError(err)
		return
	}
	if !ok {
		c.JSON(404, obj("error", "Equipo no encontrado"))
		return
	}
	c.JSON(200, obj("message", "Equipo eliminado exitosamente"))
}

// ---------- Auth ----------

// iamUsername consulta /auth/me del BFF con las cookies del navegador.
func (a *App) iamUsername(r *http.Request) *string {
	cookie := strings.Join(r.Header.Values("Cookie"), "; ")
	if cookie == "" {
		return nil
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, a.cfg.IAMMeURL, nil)
	if err != nil {
		return nil
	}
	req.Header.Set("Cookie", cookie)
	req.Header.Set("Accept", "application/json")
	res, err := a.http.Do(req)
	if err != nil {
		return nil
	}
	defer res.Body.Close()
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return nil
	}
	var data any
	if err := json.NewDecoder(res.Body).Decode(&data); err != nil {
		return nil
	}
	m, ok := data.(map[string]any)
	if !ok {
		return nil
	}
	profile, _ := m["profile"].(map[string]any)
	preferred := profile["preferred_username"]
	if !truthy(preferred) {
		preferred = m["preferred_username"]
	}
	s, ok := preferred.(string)
	if !ok || s == "" {
		return nil
	}
	s = strings.ReplaceAll(s, ".", "")
	return &s
}

func (a *App) iamLocalBypass(r *http.Request) bool {
	if !a.cfg.IAMLocalBypass {
		return false
	}
	host := r.Host
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	return slices.Contains(a.cfg.IAMLocalBypassHost, host)
}

// POST auth/login/
func (a *App) login(c *Ctx) {
	data, ok := c.data()
	if !ok {
		return
	}
	username := data["username"]
	rememberMe := data["remember_me"]

	if !truthy(username) {
		c.JSON(401, obj("error", "Credenciales incorrectas"))
		return
	}
	if a.cfg.IAMVerifyLogin && !a.iamLocalBypass(c.r) {
		iam := a.iamUsername(c.r)
		us, isStr := username.(string)
		if iam == nil || !isStr || *iam != us {
			c.JSON(401, obj("error", "Sesión del IAM inválida o no corresponde al usuario"))
			return
		}
	}

	u, err := scanUser(a.db.QueryRow(c.ctx(), userSelect+` WHERE u.username = $1 AND u.is_active ORDER BY u.id LIMIT 1`, pyStr(username)))
	if isNoRows(err) {
		c.JSON(401, obj("error", "Credenciales incorrectas"))
		return
	}
	if err != nil {
		c.serverError(err)
		return
	}

	remember := truthy(rememberMe)
	accessLifetime, refreshLifetime := 15*time.Minute, 24*time.Hour
	maxAgeAccess, maxAgeRefresh := 900, 86400
	if remember {
		accessLifetime, refreshLifetime = 24*time.Hour, 30*24*time.Hour
		maxAgeAccess, maxAgeRefresh = 86400, 432000
	}

	refresh, err := a.newRefreshToken(c.ctx(), u.ID)
	if err != nil {
		c.serverError(err)
		return
	}
	// Como en Django, el access token siempre usa ACCESS_TOKEN_LIFETIME (15 min):
	// el set_exp sobre refresh.access_token no afectaba al token que se devolvía.
	access := a.accessFrom(refresh.userID, refresh.iat, refresh.created)

	now := time.Now()
	var tipo, tipoID any
	if u.TipoID != nil {
		tipo, tipoID = ptrAny(u.TipoDesc), *u.TipoID
	}
	resp := obj(
		"message", "Inicio de sesión exitoso",
		"usuario", obj("id", u.ID, "name", u.Name, "username", u.Username, "email", u.Email, "tipo", tipo, "tipo_id", tipoID),
		"token_info", obj(
			"access_token_expires_in", maxAgeAccess,
			"refresh_token_expires_in", maxAgeRefresh,
			"access_token_expires_at", naiveISO(now.Add(accessLifetime)),
			"refresh_token_expires_at", naiveISO(now.Add(refreshLifetime)),
			"remember_me", rememberMe,
		),
	)
	c.setCookie(cookieAccessName, access, maxAgeAccess)
	c.setCookie(cookieRefreshName, refresh.raw, maxAgeRefresh)
	c.JSON(200, resp)
}

// POST auth/logout/
func (a *App) logout(c *Ctx) {
	if ck, err := c.r.Cookie(cookieRefreshName); err == nil && ck.Value != "" {
		// En Django un refresh inválido provocaba un 500; aquí se ignora y se cierran igual las cookies
		if claims, msg := a.verifyToken(c.ctx(), ck.Value, "refresh"); msg == "" {
			if err := a.blacklist(c.ctx(), ck.Value, claims); err != nil {
				c.serverError(err)
				return
			}
		}
	}
	c.deleteCookie(cookieAccessName)
	c.deleteCookie(cookieRefreshName)
	c.JSON(200, obj("ok", true))
}

// GET usuarios/me/
func (a *App) me(c *Ctx) {
	u := c.user
	c.JSON(200, obj("id", u.ID, "name", u.Name, "username", u.Username, "email", u.Email, "tipo_id", ptrAny(u.TipoID)))
}

// POST auth/refresh/
func (a *App) refreshTokenView(c *Ctx) {
	ck, err := c.r.Cookie(cookieRefreshName)
	if err != nil || ck.Value == "" {
		c.JSON(401, obj("error", "No se encontró refresh token"))
		return
	}
	claims, msg := a.verifyToken(c.ctx(), ck.Value, "refresh")
	if msg != "" {
		c.JSON(401, obj("error", "Token inválido o expirado"))
		return
	}
	now := time.Now()
	access := a.accessFrom(jsonInt64(claims["user_id"]), jsonInt64(claims["iat"]), now)

	exp, _ := pyInt(claims["exp"])
	daysRemaining := math.Floor((float64(exp) - float64(now.UnixMicro())/1e6) / 86400)
	remember := daysRemaining > 7

	maxAgeAccess, maxAgeRefresh := 900, 86400
	accessLifetime, refreshLifetime := 15*time.Minute, 24*time.Hour
	if remember {
		maxAgeAccess, maxAgeRefresh = 86400, 432000
		accessLifetime, refreshLifetime = 24*time.Hour, 5*24*time.Hour
	}
	c.setCookie(cookieAccessName, access, maxAgeAccess)
	c.JSON(200, obj(
		"message", "Token refrescado",
		"token_info", obj(
			"access_token_expires_in", maxAgeAccess,
			"refresh_token_expires_in", maxAgeRefresh,
			"access_token_expires_at", naiveISO(now.Add(accessLifetime)),
			"refresh_token_expires_at", naiveISO(now.Add(refreshLifetime)),
			"remember_me", remember,
		),
	))
}

// ---------- usuarios.datosMedicos ----------

var datosMedicosFields = []string{"run", "prevision", "direccion", "contactoEmergencia", "lesiones", "enfermedades", "alergias"}

// PUT/PATCH usuarios/datosMedicos/<int:idUser>
func (a *App) datosMedicosUpdate(c *Ctx) {
	idUser := c.intParam("idUser")
	if !(c.user.ID == idUser || isAdmin(c.user)) {
		c.JSON(403, obj("message", "No tienes permisos para modificar estos datos"))
		return
	}
	data, ok := c.data()
	if !ok {
		return
	}
	ctx := c.ctx()
	tx, err := a.db.Begin(ctx)
	if err != nil {
		c.serverError(err)
		return
	}
	defer tx.Rollback(ctx)

	rows, err := tx.Query(ctx, `SELECT id FROM usuarios_datosmedicos WHERE "idUser_id" = $1 LIMIT 21 FOR UPDATE`, idUser)
	if err != nil {
		c.serverError(err)
		return
	}
	ids, err := pgx.CollectRows(rows, pgx.RowTo[int64])
	if err != nil {
		c.serverError(err)
		return
	}
	if len(ids) > 1 {
		c.serverError(fmt.Errorf("get() returned more than one DatosMedicos -- it returned %d!", len(ids)))
		return
	}

	created := len(ids) == 0
	var id int64
	if created {
		vals := map[string]any{"run": "", "prevision": "", "direccion": "", "contactoEmergencia": "", "lesiones": "",
			"enfermedades": nil, "alergias": nil}
		for _, f := range datosMedicosFields {
			if v, ok := data[f]; ok {
				vals[f] = dbText(v)
			}
		}
		err = tx.QueryRow(ctx, `INSERT INTO usuarios_datosmedicos ("idUser_id", run, prevision, direccion, "contactoEmergencia",
			lesiones, enfermedades, alergias) VALUES ($1, $2::text, $3::text, $4::text, $5::text, $6::text, $7::text, $8::text) RETURNING id`,
			idUser, vals["run"], vals["prevision"], vals["direccion"], vals["contactoEmergencia"],
			vals["lesiones"], vals["enfermedades"], vals["alergias"]).Scan(&id)
	} else {
		id = ids[0]
		var sets []string
		args := []any{}
		for _, f := range datosMedicosFields {
			if v, ok := data[f]; ok {
				args = append(args, dbText(v))
				sets = append(sets, fmt.Sprintf(`%q = $%d::text`, f, len(args)))
			}
		}
		if len(sets) > 0 {
			args = append(args, id)
			_, err = tx.Exec(ctx, fmt.Sprintf(`UPDATE usuarios_datosmedicos SET %s WHERE id = $%d`, strings.Join(sets, ", "), len(args)), args...)
		}
	}
	if err == nil {
		err = tx.Commit(ctx)
	}
	if err != nil {
		c.serverError(err)
		return
	}
	c.JSON(200, obj("ok", true, "created", created, "id", id))
}

// GET usuarios/getDatosMedicos/<int:idUser>
func (a *App) datosMedicosGet(c *Ctx) {
	idUser := c.intParam("idUser")
	if !isSelfOrStaff(c.user, idUser) {
		c.JSON(403, obj("message", "No tienes permisos para ver estos datos"))
		return
	}
	var id int64
	var uid *int64
	var run, prevision, direccion, contacto, lesiones string
	var enfermedades, alergias *string
	err := a.db.QueryRow(c.ctx(), `SELECT id, "idUser_id", run, prevision, direccion, "contactoEmergencia", lesiones,
		enfermedades, alergias FROM usuarios_datosmedicos WHERE "idUser_id" = $1 ORDER BY id LIMIT 1`, idUser).
		Scan(&id, &uid, &run, &prevision, &direccion, &contacto, &lesiones, &enfermedades, &alergias)
	if isNoRows(err) {
		c.JSON(404, obj("message", "Datos médicos no encontrados"))
		return
	}
	if err != nil {
		c.serverError(err)
		return
	}
	c.JSON(200, obj("id", id, "idUser", ptrAny(uid), "run", run, "prevision", prevision, "direccion", direccion,
		"contactoEmergencia", contacto, "lesiones", lesiones, "enfermedades", ptrAny(enfermedades), "alergias", ptrAny(alergias)))
}
