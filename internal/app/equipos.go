package app

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// ---------- equipos.views ----------

// POST equipos/asignarEquipo/
func (a *App) asignarEquipo(c *Ctx) {
	body, err := c.loadsObj()
	if err != nil {
		c.serverError(err)
		return
	}
	teamID := body["team_id"]
	userIDs := body["user_ids"]
	created := []any{}
	skipped := []int64{}
	missing := []any{}
	var errMsg any
	result := func() OM {
		return obj("team_id", teamID, "requested_user_ids", userIDs, "created", created,
			"skipped_already_assigned", skipped, "missing_users", missing, "error", errMsg)
	}
	ctx := c.ctx()

	var team int64
	err = pgx.ErrNoRows
	if teamID != nil {
		err = a.db.QueryRow(ctx, `SELECT id FROM usuarios_equipos WHERE id = $1::text::int`, dbText(teamID)).Scan(&team)
	}
	if isNoRows(err) {
		errMsg = fmt.Sprintf("Team con id %s no existe.", pyStr(teamID))
		c.JSON(200, result())
		return
	}
	if err != nil {
		c.serverError(err)
		return
	}

	list, ok := userIDs.([]any)
	if !ok {
		c.serverError(errors.New("user_ids debe ser una lista"))
		return
	}
	ids := make([]int64, 0, len(list))
	for _, v := range list {
		n, ok := pyInt(v)
		if !ok {
			c.serverError(fmt.Errorf("id inválido: %v", v))
			return
		}
		ids = append(ids, n)
	}

	rows, err := a.db.Query(ctx, `SELECT id FROM usuarios_usuarios WHERE id = ANY($1)`, ids)
	if err != nil {
		c.serverError(err)
		return
	}
	users, err := pgx.CollectRows(rows, pgx.RowTo[int64])
	if err != nil {
		c.serverError(err)
		return
	}
	for _, v := range list {
		// En Python "uid not in existing_user_ids" compara por igualdad: "5" no es 5
		n, isNum := intKey(v)
		if b, isBool := v.(bool); isBool {
			n, isNum = 0, true
			if b {
				n = 1
			}
		}
		if !isNum || !slices.Contains(users, n) {
			missing = append(missing, v)
		}
	}
	if len(users) == 0 {
		c.JSON(200, result())
		return
	}

	rows, err = a.db.Query(ctx, `SELECT DISTINCT "idUser_id" FROM usuarios_usersteams
		WHERE "idTeam_id" = $1 AND "idUser_id" = ANY($2) ORDER BY 1`, team, users)
	if err != nil {
		c.serverError(err)
		return
	}
	if skipped, err = pgx.CollectRows(rows, pgx.RowTo[int64]); err != nil {
		c.serverError(err)
		return
	}

	var toCreate []int64
	for _, u := range users {
		if !slices.Contains(skipped, u) {
			toCreate = append(toCreate, u)
		}
	}
	if len(toCreate) == 0 {
		c.JSON(200, result())
		return
	}
	tx, err := a.db.Begin(ctx)
	if err != nil {
		c.serverError(err)
		return
	}
	defer tx.Rollback(ctx)
	for _, u := range toCreate {
		if _, err := tx.Exec(ctx, `INSERT INTO usuarios_usersteams ("idUser_id", "idTeam_id") VALUES ($1, $2)`, u, team); err != nil {
			c.serverError(err)
			return
		}
	}
	if err := tx.Commit(ctx); err != nil {
		c.serverError(err)
		return
	}
	// bulk_create(ignore_conflicts=True) no asigna ids: Django devolvía una lista de null
	for range toCreate {
		created = append(created, nil)
	}
	c.JSON(200, result())
}

// POST equipos/quitarEquipo/
func (a *App) quitarEquipo(c *Ctx) {
	body, err := c.loadsObj()
	if errors.Is(err, errNotObject) {
		c.serverError(err)
		return
	}
	teamID, ok1 := pyInt(body["team_id"])
	userID, ok2 := pyInt(body["user_id"])
	if err != nil || !ok1 || !ok2 {
		c.JSON(400, obj("message", "team_id y user_id son obligatorios"))
		return
	}
	tag, err := a.db.Exec(c.ctx(), `DELETE FROM usuarios_usersteams WHERE "idTeam_id" = $1 AND "idUser_id" = $2`, teamID, userID)
	if err != nil {
		c.serverError(err)
		return
	}
	if tag.RowsAffected() == 0 {
		c.JSON(404, obj("message", "El usuario no pertenece al equipo"))
		return
	}
	c.JSON(200, obj("message", "Usuario quitado del equipo"))
}

// GET equipos/listAsignadosEquipo/<int:idTeam>
func (a *App) verAsignadosEquipo(c *Ctx) {
	idTeam := c.intParam("idTeam")
	ctx := c.ctx()
	var exists bool
	if err := a.db.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM usuarios_equipos WHERE id = $1)`, idTeam).Scan(&exists); err != nil {
		c.serverError(err)
		return
	}
	if !exists {
		c.JSON(404, obj("message", fmt.Sprintf("Equipo con id %d no encontrado", idTeam), "team_id", idTeam, "data", []any{}))
		return
	}
	// Incluye el rol para que el front agrupe a deportistas, coaches y staff
	rows, err := a.db.Query(ctx, `SELECT u.id, u.name, u.username, u.email, u.tipo_id, t.description, u.perfiles FROM usuarios_usersteams ut
		JOIN usuarios_usuarios u ON u.id = ut."idUser_id"
		LEFT JOIN usuarios_tipousuario t ON t.id = u.tipo_id
		WHERE ut."idTeam_id" = $1 ORDER BY u.name`, idTeam)
	if err != nil {
		c.serverError(err)
		return
	}
	defer rows.Close()
	data := []OM{}
	for rows.Next() {
		var id int64
		var name, username, email string
		var tipoID *int64
		var tipo *string
		var perfiles []int64
		if err := rows.Scan(&id, &name, &username, &email, &tipoID, &tipo, &perfiles); err != nil {
			c.serverError(err)
			return
		}
		data = append(data, obj("id", id, "name", name, "username", username, "email", email,
			"tipo_id", ptrAny(tipoID), "tipo", ptrAny(tipo), "perfiles", perfilesJSON(perfiles)))
	}
	if err := rows.Err(); err != nil {
		c.serverError(err)
		return
	}
	if len(data) == 0 {
		c.JSON(200, obj("message", fmt.Sprintf("No hay usuarios asignados al equipo %d", idTeam), "team_id", idTeam, "data", []any{}))
		return
	}
	c.JSON(200, obj("message", "Usuarios encontrados", "team_id", idTeam, "data", data))
}

// GET equipos/getTeamsByUser/<int:user_id>
func (a *App) obtenerEquiposByUser(c *Ctx) {
	userID := c.intParam("user_id")
	ctx := c.ctx()
	var exists bool
	if err := a.db.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM usuarios_usersteams WHERE "idUser_id" = $1)`, userID).Scan(&exists); err != nil {
		c.serverError(err)
		return
	}
	if !exists {
		c.JSON(400, obj("message", fmt.Sprintf("Usuario con id %d no encontrado", userID), "data", []any{}))
		return
	}
	rows, err := a.db.Query(ctx, `SELECT e.id, e.description, e.category, e.level, e.division, e.color
		FROM usuarios_usersteams ut JOIN usuarios_equipos e ON e.id = ut."idTeam_id" WHERE ut."idUser_id" = $1`, userID)
	if err != nil {
		c.serverError(err)
		return
	}
	defer rows.Close()
	data := []OM{}
	for rows.Next() {
		var id int64
		var description, category, level string
		var division, color *string
		if err := rows.Scan(&id, &description, &category, &level, &division, &color); err != nil {
			c.serverError(err)
			return
		}
		data = append(data, obj("id", id, "description", description, "category", category, "level", level,
			"division", ptrAny(division), "color", ptrAny(color)))
	}
	if err := rows.Err(); err != nil {
		c.serverError(err)
		return
	}
	c.JSON(200, obj("message", "Equipos encontrados", "data", data))
}

// ---------- eventos.views ----------

// qLast replica request.query_params.get(): devuelve el último valor de la clave.
func qLast(c *Ctx, key string) string {
	vs := c.r.URL.Query()[key]
	if len(vs) == 0 {
		return ""
	}
	return vs[len(vs)-1]
}

// GET eventos/getUsersEvents/<int:team_id>
func (a *App) getTeamEvents(c *Ctx) {
	teamID := c.intParam("team_id")
	ctx := c.ctx()
	var id int64
	var description, category, level string
	var division *string
	err := a.db.QueryRow(ctx, `SELECT id, description, category, level, division FROM usuarios_equipos WHERE id = $1`, teamID).
		Scan(&id, &description, &category, &level, &division)
	if isNoRows(err) {
		c.JSON(404, obj("error", "Equipo no encontrado"))
		return
	}
	if err != nil {
		c.serverError(err)
		return
	}

	q := `SELECT id, titulo, descripcion, tipo, fecha_inicio, fecha_fin, ubicacion, es_recurrente, frecuencia
		FROM eventos_evento WHERE equipo_id = $1`
	args := []any{teamID}
	if v := qLast(c, "tipo"); v != "" {
		args = append(args, v)
		q += fmt.Sprintf(` AND tipo = $%d`, len(args))
	}
	if v := qLast(c, "fecha_desde"); v != "" {
		args = append(args, v)
		q += fmt.Sprintf(` AND fecha_inicio >= $%d::text::timestamptz`, len(args))
	}
	if v := qLast(c, "fecha_hasta"); v != "" {
		args = append(args, v)
		q += fmt.Sprintf(` AND fecha_inicio <= $%d::text::timestamptz`, len(args))
	}
	q += ` ORDER BY fecha_inicio`

	rows, err := a.db.Query(ctx, q, args...)
	if err != nil {
		c.serverError(err)
		return
	}
	defer rows.Close()
	eventos := []OM{}
	for rows.Next() {
		var eid int64
		var titulo, desc, tipo, ubicacion, frecuencia string
		var ini, fin time.Time
		var rec bool
		if err := rows.Scan(&eid, &titulo, &desc, &tipo, &ini, &fin, &ubicacion, &rec, &frecuencia); err != nil {
			c.serverError(err)
			return
		}
		eventos = append(eventos, obj("id", eid, "titulo", titulo, "descripcion", desc, "tipo", tipo,
			"fecha_inicio", drfISO(ini), "fecha_fin", drfISO(fin), "ubicacion", ubicacion,
			"es_recurrente", rec, "frecuencia", frecuencia))
	}
	if err := rows.Err(); err != nil {
		c.serverError(err)
		return
	}
	c.JSON(200, obj(
		"equipo", obj("id", id, "description", description, "category", category, "level", level, "division", ptrAny(division)),
		"total_eventos", len(eventos),
		"eventos", eventos,
	))
}

// GET eventos/listEventos/
func (a *App) getEvents(c *Ctx) {
	var idteams []any
	qp := c.r.URL.Query()
	if raw, ok := qp["idteams"]; ok {
		if len(raw) == 1 && strings.Contains(raw[0], ",") {
			var parts []string
			for _, x := range strings.Split(raw[0], ",") {
				if x = strings.TrimSpace(x); x != "" {
					parts = append(parts, x)
				}
			}
			raw = parts
		}
		for _, x := range raw {
			idteams = append(idteams, x)
		}
	}
	if len(idteams) == 0 {
		data, ok := c.data()
		if !ok {
			return
		}
		if bv, ok := data["idteams"]; ok && bv != nil {
			switch t := bv.(type) {
			case string:
				for _, x := range strings.Split(t, ",") {
					if x = strings.TrimSpace(x); x != "" {
						idteams = append(idteams, x)
					}
				}
			case []any:
				idteams = t
			default:
				idteams = []any{bv}
			}
		}
	}
	var teamIDs []int64
	for _, x := range idteams {
		if n, ok := pyInt(x); ok {
			teamIDs = append(teamIDs, n)
		}
	}

	q := `SELECT ev.id, ev.titulo, ev.descripcion, ev.tipo, ev.fecha_inicio, ev.fecha_fin, ev.ubicacion,
		ev.creado_en, ev.actualizado_en, e.id, e.description, e.color
		FROM eventos_evento ev JOIN usuarios_equipos e ON e.id = ev.equipo_id`
	var args []any
	if len(teamIDs) > 0 {
		q += ` WHERE ev.equipo_id = ANY($1)`
		args = append(args, teamIDs)
	}
	q += ` ORDER BY ev.fecha_inicio`
	rows, err := a.db.Query(c.ctx(), q, args...)
	if err != nil {
		c.serverError(err)
		return
	}
	defer rows.Close()
	data := []OM{}
	for rows.Next() {
		var id, eqID int64
		var titulo, desc, tipo, ubicacion, eqDesc string
		var color *string
		var ini, fin, creado, actualizado time.Time
		if err := rows.Scan(&id, &titulo, &desc, &tipo, &ini, &fin, &ubicacion, &creado, &actualizado, &eqID, &eqDesc, &color); err != nil {
			c.serverError(err)
			return
		}
		data = append(data, obj("id", id, "title", titulo, "description", desc, "tipo", tipo,
			"start", pyISO(ini), "end", pyISO(fin), "ubicacion", ubicacion,
			"creado_en", pyISO(creado), "actualizado_en", pyISO(actualizado),
			"equipo", obj("id", eqID, "description", eqDesc, "color", ptrAny(color))))
	}
	if err := rows.Err(); err != nil {
		c.serverError(err)
		return
	}
	c.JSON(200, obj("message", "Eventos listados con éxito", "data", data))
}

// POST eventos/crearEvento/
// datosEvento valida los campos de un evento y los devuelve listos para guardar.
// En "parcial" (edición) solo se validan los campos presentes; el resto conserva su valor actual.
func (a *App) datosEvento(ctx context.Context, data map[string]any, actual map[string]any) (map[string]any, string, error) {
	v := map[string]any{}
	for k, x := range actual {
		v[k] = x
	}
	for _, k := range []string{"equipo_id", "titulo", "descripcion", "tipo", "fecha_inicio", "fecha_fin", "ubicacion"} {
		if x, ok := data[k]; ok {
			v[k] = x
		}
	}
	titulo := strings.TrimSpace(pyStr(v["titulo"]))
	if v["titulo"] == nil || titulo == "" {
		return nil, "El título es obligatorio", nil
	}
	if len([]rune(titulo)) > 200 {
		return nil, "El título no puede tener más de 200 caracteres", nil
	}
	v["titulo"] = titulo
	equipo, ok := pyInt(v["equipo_id"])
	if !ok {
		return nil, "Selecciona el equipo", nil
	}
	var existe bool
	if err := a.db.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM usuarios_equipos WHERE id = $1)`, equipo).Scan(&existe); err != nil {
		return nil, "", err
	}
	if !existe {
		return nil, "El equipo no existe", nil
	}
	v["equipo_id"] = equipo
	ini, okIni := parseISO(strings.ReplaceAll(pyStr(v["fecha_inicio"]), "Z", "+00:00"))
	fin, okFin := parseISO(strings.ReplaceAll(pyStr(v["fecha_fin"]), "Z", "+00:00"))
	if !okIni || !okFin {
		return nil, "Indica fecha y hora de inicio y término", nil
	}
	if !fin.After(ini) {
		return nil, "El término debe ser posterior al inicio", nil
	}
	v["fecha_inicio"], v["fecha_fin"] = ini, fin
	if v["tipo"] == nil || strings.TrimSpace(pyStr(v["tipo"])) == "" {
		v["tipo"] = "Entrenamiento"
	}
	v["descripcion"] = strings.TrimSpace(pyStr(or0(v["descripcion"])))
	v["ubicacion"] = strings.TrimSpace(pyStr(or0(v["ubicacion"])))
	return v, "", nil
}

// or0 convierte nil en "" para los textos opcionales
func or0(x any) any {
	if x == nil {
		return ""
	}
	return x
}

// POST eventos/crearEvento/
func (a *App) createEvent(c *Ctx) {
	data, ok := c.data()
	if !ok {
		return
	}
	v, msg, err := a.datosEvento(c.ctx(), data, nil)
	if err != nil {
		c.serverError(err)
		return
	}
	if msg != "" {
		c.JSON(400, obj("message", msg))
		return
	}
	now := nowUTC()
	var id int64
	err = a.db.QueryRow(c.ctx(), `INSERT INTO eventos_evento (equipo_id, titulo, descripcion, tipo, fecha_inicio, fecha_fin,
		ubicacion, creado_en, actualizado_en, es_recurrente, frecuencia)
		VALUES ($1, $2::text, $3::text, $4::text, $5, $6, $7::text, $8, $8, false, '') RETURNING id`,
		v["equipo_id"], v["titulo"], dbText(v["descripcion"]), dbText(v["tipo"]),
		v["fecha_inicio"], v["fecha_fin"], dbText(v["ubicacion"]), now).Scan(&id)
	if err != nil {
		c.serverError(err)
		return
	}
	c.JSON(200, obj("message", "Evento creado exitosamente", "id", id))
}

// PUT eventos/editEvento/<int:id>: cambia los datos del evento (los campos que no vienen se mantienen)
func (a *App) editEvent(c *Ctx) {
	id := c.intParam("id")
	var equipo int64
	var titulo, desc, tipo, ubicacion string
	var ini, fin time.Time
	err := a.db.QueryRow(c.ctx(), `SELECT equipo_id, titulo, descripcion, tipo, fecha_inicio, fecha_fin, ubicacion
		FROM eventos_evento WHERE id = $1`, id).Scan(&equipo, &titulo, &desc, &tipo, &ini, &fin, &ubicacion)
	if isNoRows(err) {
		c.JSON(404, obj("message", "Evento no encontrado"))
		return
	}
	if err != nil {
		c.serverError(err)
		return
	}
	data, ok := c.data()
	if !ok {
		return
	}
	actual := map[string]any{"equipo_id": equipo, "titulo": titulo, "descripcion": desc, "tipo": tipo,
		"fecha_inicio": pyISO(ini), "fecha_fin": pyISO(fin), "ubicacion": ubicacion}
	v, msg, err := a.datosEvento(c.ctx(), data, actual)
	if err != nil {
		c.serverError(err)
		return
	}
	if msg != "" {
		c.JSON(400, obj("message", msg))
		return
	}
	_, err = a.db.Exec(c.ctx(), `UPDATE eventos_evento SET equipo_id = $1, titulo = $2::text, descripcion = $3::text, tipo = $4::text,
		fecha_inicio = $5, fecha_fin = $6, ubicacion = $7::text, actualizado_en = $8 WHERE id = $9`,
		v["equipo_id"], v["titulo"], dbText(v["descripcion"]), dbText(v["tipo"]), v["fecha_inicio"], v["fecha_fin"],
		dbText(v["ubicacion"]), nowUTC(), id)
	if err != nil {
		c.serverError(err)
		return
	}
	c.JSON(200, obj("message", "Evento actualizado", "id", id))
}

// DELETE eventos/deleteEvento/<int:id>
func (a *App) deleteEvento(c *Ctx) {
	ok, err := a.deleteByID(c.ctx(), "eventos_evento", c.intParam("id"))
	if err != nil {
		c.serverError(err)
		return
	}
	if !ok {
		c.JSON(404, obj("error", "Evento no encontrado"))
		return
	}
	c.JSON(200, obj("message", "Evento eliminado exitosamente"))
}
