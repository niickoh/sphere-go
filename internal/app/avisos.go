package app

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"slices"
	"sort"
	"strings"
	"time"

	webpush "github.com/SherClockHolmes/webpush-go"
	"github.com/jackc/pgx/v5"
)

// GET avisos/listar/
func (a *App) listarAvisos(c *Ctx) {
	rows, err := a.db.Query(c.ctx(), `SELECT av.id, av.titulo, av.contenido, av.creado_por_id, u.name, t.description, av.fecha_creacion
		FROM avisos_aviso av
		LEFT JOIN usuarios_usuarios u ON u.id = av.creado_por_id
		LEFT JOIN usuarios_tipousuario t ON t.id = u.tipo_id
		ORDER BY av.fecha_creacion DESC`)
	if err != nil {
		c.serverError(err)
		return
	}
	defer rows.Close()
	data := []OM{}
	for rows.Next() {
		var id int64
		var titulo, contenido string
		var creadoPor *int64
		var nombre, rol *string
		var fecha time.Time
		if err := rows.Scan(&id, &titulo, &contenido, &creadoPor, &nombre, &rol, &fecha); err != nil {
			c.serverError(err)
			return
		}
		data = append(data, obj("id", id, "titulo", titulo, "contenido", contenido, "creado_por", ptrAny(creadoPor),
			"creado_por_nombre", ptrAny(nombre), "creado_por_rol", ptrAny(rol), "fecha_creacion", drfISO(fecha)))
	}
	if err := rows.Err(); err != nil {
		c.serverError(err)
		return
	}
	c.JSON(200, data)
}

// POST avisos/add/
func (a *App) addAviso(c *Ctx) {
	raw := c.rawBody()
	if len(raw) == 0 {
		raw = []byte("{}")
	}
	v, err := decodeJSON(raw)
	if err != nil {
		c.JSON(400, obj("error", "Solicitud inválida"))
		return
	}
	body, ok := v.(map[string]any)
	if !ok {
		c.JSON(500, obj("error", "Error interno", "detail", fmt.Sprintf("'%s' object has no attribute 'get'", pyTypeName(v))))
		return
	}
	titulo, contenido := body["titulo"], body["contenido"]
	targetAll := truthy(body["target_all"])
	userIDs := body["user_ids"]
	if !truthy(userIDs) {
		userIDs = []any{}
	}
	if !truthy(titulo) || !truthy(contenido) {
		c.JSON(400, obj("message", "titulo y contenido son obligatorios"))
		return
	}
	// request.user siempre es un Usuarios autenticado (IsClubAdmin), así que no se usa creado_por del body
	creador := c.user.ID
	if !targetAll && !truthy(userIDs) {
		c.JSON(400, obj("message", "Debes indicar destinatarios: target_all, user_ids, group_ids o role_ids"))
		return
	}

	ctx := c.ctx()
	fail := func(err error) {
		c.JSON(500, obj("error", "Error interno", "detail", pgErrDetail(err)))
	}
	tx, err := a.db.Begin(ctx)
	if err != nil {
		fail(err)
		return
	}
	defer tx.Rollback(ctx)

	var avisoID int64
	var fecha time.Time
	err = tx.QueryRow(ctx, `INSERT INTO avisos_aviso (titulo, contenido, creado_por_id, fecha_creacion)
		VALUES ($1::text, $2::text, $3, $4) RETURNING id, fecha_creacion`, dbText(titulo), dbText(contenido), creador, nowUTC()).Scan(&avisoID, &fecha)
	if err != nil {
		fail(err)
		return
	}

	recipients := map[int64]bool{}
	addIDs := func(q string, args ...any) error {
		rows, err := tx.Query(ctx, q, args...)
		if err != nil {
			return err
		}
		ids, err := pgx.CollectRows(rows, pgx.RowTo[int64])
		for _, id := range ids {
			recipients[id] = true
		}
		return err
	}
	if targetAll {
		if err := addIDs(`SELECT id FROM usuarios_usuarios`); err != nil {
			fail(err)
			return
		}
	}
	if truthy(userIDs) {
		list, ok := userIDs.([]any)
		if !ok {
			fail(fmt.Errorf("'%s' object is not iterable", pyTypeName(userIDs)))
			return
		}
		var ids []int64
		for _, x := range list {
			n, ok := pyInt(x)
			if !ok {
				fail(fmt.Errorf("Field 'id' expected a number but got %s.", pyRepr(x)))
				return
			}
			ids = append(ids, n)
		}
		if err := addIDs(`SELECT id FROM usuarios_usuarios WHERE id = ANY($1)`, ids); err != nil {
			fail(err)
			return
		}
	}
	if len(recipients) == 0 {
		// Como en Django: el return ocurre dentro de transaction.atomic, así que el aviso queda guardado
		if err := tx.Commit(ctx); err != nil {
			fail(err)
			return
		}
		c.JSON(400, obj("message", "No se encontraron destinatarios válidos"))
		return
	}

	ids := make([]int64, 0, len(recipients))
	for id := range recipients {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	now := nowUTC()
	for _, uid := range ids {
		if _, err := tx.Exec(ctx, `INSERT INTO avisos_avisodestinatario ("idUser_id", "idAviso_id", fecha_envio, fecha_leido)
			VALUES ($1, $2, $3, NULL)`, uid, avisoID, now); err != nil {
			fail(err)
			return
		}
	}
	if err := tx.Commit(ctx); err != nil {
		fail(err)
		return
	}

	metrics := a.sendPushToUsers(ctx, ids, obj("type", "aviso", "avisoId", avisoID, "title", titulo, "body", contenido, "url", "avisos"))
	c.JSON(201, obj(
		"message", "Aviso creado con éxito",
		"aviso", obj("id", avisoID, "titulo", titulo, "contenido", contenido, "creado_por", creador,
			"fecha_creacion", drfISO(fecha), "destinatarios_creados", len(ids)),
		"metrics", metrics,
	))
}

// PUT avisos/edit/<int:id>
func (a *App) editAviso(c *Ctx) {
	id := c.intParam("id")
	ctx := c.ctx()
	var titulo, contenido string
	var creadoPor *int64
	err := a.db.QueryRow(ctx, `SELECT titulo, contenido, creado_por_id FROM avisos_aviso WHERE id = $1`, id).Scan(&titulo, &contenido, &creadoPor)
	if isNoRows(err) {
		c.JSON(404, obj("error", "Aviso no encontrado"))
		return
	}
	if err != nil {
		c.serverError(err)
		return
	}
	body, err := c.loadsObj()
	if errors.Is(err, errNotObject) {
		c.serverError(err)
		return
	}
	if err != nil {
		c.JSON(400, obj("error", "Solicitud inválida"))
		return
	}
	var t, ct any = titulo, contenido
	cp := ptrAny(creadoPor)
	if v, ok := body["titulo"]; ok {
		t = v
	}
	if v, ok := body["contenido"]; ok {
		ct = v
	}
	if v, ok := body["creado_por"]; ok {
		var uid int64
		err := pgx.ErrNoRows
		if v != nil {
			err = a.db.QueryRow(ctx, `SELECT id FROM usuarios_usuarios WHERE id = $1::text::int`, dbText(v)).Scan(&uid)
		}
		if err != nil {
			c.JSON(404, obj("error", "El usuario no existe"))
			return
		}
		cp = uid
	}
	if _, err := a.db.Exec(ctx, `UPDATE avisos_aviso SET titulo = $1::text, contenido = $2::text, creado_por_id = $3 WHERE id = $4`,
		dbText(t), dbText(ct), cp, id); err != nil {
		c.serverError(err)
		return
	}
	c.JSON(200, obj("message", "Aviso actualizado con éxito", "aviso", obj("id", id, "titulo", t, "contenido", ct, "creado_por", cp)))
}

// DELETE avisos/delete/<int:id>
func (a *App) deleteAviso(c *Ctx) {
	ok, err := a.deleteByID(c.ctx(), "avisos_aviso", c.intParam("id"))
	if err != nil {
		c.serverError(err)
		return
	}
	if !ok {
		c.JSON(404, obj("error", "Aviso no encontrado"))
		return
	}
	c.JSON(200, obj("message", "Aviso eliminado con éxito"))
}

// GET avisos/mis-avisos/<int:user>/<str:unread>
func (a *App) misAvisos(c *Ctx) {
	idUser := c.intParam("user")
	if idUser == 0 {
		c.JSON(400, obj("message", "Parámetro 'user' es obligatorio"))
		return
	}
	q := `SELECT av.id, av.titulo, av.contenido, av.creado_por_id, av.fecha_creacion, ad.fecha_envio, ad.fecha_leido
		FROM avisos_avisodestinatario ad
		JOIN avisos_aviso av ON av.id = ad."idAviso_id"
		WHERE ad."idUser_id" = $1`
	switch strings.ToLower(strings.TrimSpace(c.params["unread"])) {
	case "1", "true", "yes":
		q += ` AND ad.fecha_leido IS NULL`
	case "0", "false", "no":
		q += ` AND ad.fecha_leido IS NOT NULL`
	default:
		c.JSON(400, obj("message", "Parámetro 'unread' inválido (use true/false)"))
		return
	}
	// Los destinatarios de avisos borrados (idAviso NULL) hacían fallar la vista en Django; aquí se omiten
	q += ` ORDER BY av.fecha_creacion DESC`
	rows, err := a.db.Query(c.ctx(), q, idUser)
	if err != nil {
		c.serverError(err)
		return
	}
	defer rows.Close()
	data := []OM{}
	for rows.Next() {
		var id int64
		var titulo, contenido string
		var creadoPor *int64
		var fecha time.Time
		var envio, leido *time.Time
		if err := rows.Scan(&id, &titulo, &contenido, &creadoPor, &fecha, &envio, &leido); err != nil {
			c.serverError(err)
			return
		}
		data = append(data, obj("id", id, "titulo", titulo, "contenido", contenido, "creado_por", ptrAny(creadoPor),
			"fecha_creacion", drfISO(fecha), "fecha_envio", drfISOPtr(envio), "fecha_leido", drfISOPtr(leido)))
	}
	if err := rows.Err(); err != nil {
		c.serverError(err)
		return
	}
	c.JSON(200, obj("results", data))
}

// GET avisos/unread-count/<int:user>
func (a *App) unreadCount(c *Ctx) {
	var n int64
	if err := a.db.QueryRow(c.ctx(), `SELECT COUNT(*) FROM avisos_avisodestinatario WHERE "idUser_id" = $1 AND fecha_leido IS NULL`,
		c.intParam("user")).Scan(&n); err != nil {
		c.serverError(err)
		return
	}
	c.JSON(200, obj("unread", n))
}

// POST avisos/read/<int:aviso_id>
func (a *App) marcarLeido(c *Ctx) {
	avisoID := c.intParam("aviso_id")
	ctx := c.ctx()
	now := nowUTC()
	tag, err := a.db.Exec(ctx, `UPDATE avisos_avisodestinatario SET fecha_leido = $1
		WHERE "idUser_id" = $2 AND "idAviso_id" = $3 AND fecha_leido IS NULL`, now, c.user.ID, avisoID)
	if err != nil {
		c.serverError(err)
		return
	}
	if tag.RowsAffected() == 0 {
		var exists bool
		if err := a.db.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM avisos_avisodestinatario WHERE "idUser_id" = $1 AND "idAviso_id" = $2)`,
			c.user.ID, avisoID).Scan(&exists); err != nil {
			c.serverError(err)
			return
		}
		if !exists {
			c.JSON(404, obj("message", "Aviso no encontrado para este usuario"))
			return
		}
		c.JSON(200, obj("message", "El aviso ya estaba marcado como leído"))
		return
	}
	c.JSON(200, obj("message", "Aviso marcado como leído", "fecha_leido", drfISO(now)))
}

// userFromBody replica Usuarios.objects.get(id=body.get("idUser")); un fallo era un 500 en Django.
func (a *App) userFromBody(ctx context.Context, v any) (int64, error) {
	if v == nil {
		return 0, errors.New("Usuarios matching query does not exist.")
	}
	var id int64
	err := a.db.QueryRow(ctx, `SELECT id FROM usuarios_usuarios WHERE id = $1::text::int`, dbText(v)).Scan(&id)
	return id, err
}

// POST avisos/read-all/
func (a *App) marcarTodosLeidos(c *Ctx) {
	body, err := c.loadsObj()
	if err != nil {
		c.serverError(err)
		return
	}
	uid, err := a.userFromBody(c.ctx(), body["idUser"])
	if err != nil {
		c.serverError(err)
		return
	}
	now := nowUTC()
	tag, err := a.db.Exec(c.ctx(), `UPDATE avisos_avisodestinatario SET fecha_leido = $1 WHERE "idUser_id" = $2 AND fecha_leido IS NULL`, now, uid)
	if err != nil {
		c.serverError(err)
		return
	}
	c.JSON(200, obj("message", "OK", "updated", tag.RowsAffected(), "fecha_leido", drfISO(now)))
}

func (c *Ctx) loadsOrEmpty() (map[string]any, bool) {
	raw := c.rawBody()
	if len(raw) == 0 {
		raw = []byte("{}")
	}
	v, err := decodeJSON(raw)
	if err != nil {
		c.JSON(400, obj("message", "JSON inválido"))
		return nil, false
	}
	m, ok := v.(map[string]any)
	if !ok {
		c.serverError(errNotObject)
		return nil, false
	}
	return m, true
}

// POST avisos/push/subscribe/
func (a *App) pushSubscribe(c *Ctx) {
	body, ok := c.loadsOrEmpty()
	if !ok {
		return
	}
	ctx := c.ctx()
	endpoint := body["endpoint"]
	keys, _ := body["keys"].(map[string]any)
	if !truthy(body["keys"]) {
		keys = map[string]any{}
	} else if keys == nil {
		c.serverError(errors.New("keys no es un objeto"))
		return
	}
	p256dh, auth := keys["p256dh"], keys["auth"]
	uid, err := a.userFromBody(ctx, body["idUser"])
	if err != nil {
		c.serverError(err)
		return
	}
	if !truthy(endpoint) || !truthy(p256dh) || !truthy(auth) {
		c.JSON(400, obj("message", "endpoint y keys(p256dh, auth) son obligatorios"))
		return
	}

	tx, err := a.db.Begin(ctx)
	if err != nil {
		c.serverError(err)
		return
	}
	defer tx.Rollback(ctx)
	now := nowUTC()
	var id int64
	created := false
	err = tx.QueryRow(ctx, `SELECT id FROM avisos_pushsuscriptions WHERE endpoint = $1::text FOR UPDATE`, dbText(endpoint)).Scan(&id)
	switch {
	case isNoRows(err):
		created = true
		err = tx.QueryRow(ctx, `INSERT INTO avisos_pushsuscriptions ("idUser_id", endpoint, p256dh, auth, created_at, last_seen_at)
			VALUES ($1, $2::text, $3::text, $4::text, $5, $5) RETURNING id`, uid, dbText(endpoint), dbText(p256dh), dbText(auth), now).Scan(&id)
	case err == nil:
		_, err = tx.Exec(ctx, `UPDATE avisos_pushsuscriptions SET "idUser_id" = $1, p256dh = $2::text, auth = $3::text, last_seen_at = $4
			WHERE id = $5`, uid, dbText(p256dh), dbText(auth), now, id)
	}
	if err == nil {
		err = tx.Commit(ctx)
	}
	if err != nil {
		c.serverError(err)
		return
	}
	status := 200
	if created {
		status = 201
	}
	c.JSON(status, obj("ok", true, "created", created, "id", id))
}

// DELETE avisos/push/unsubscribe/
func (a *App) pushUnsubscribe(c *Ctx) {
	body, ok := c.loadsOrEmpty()
	if !ok {
		return
	}
	endpoint := body["endpoint"]
	uid, err := a.userFromBody(c.ctx(), body["idUser"])
	if err != nil {
		c.serverError(err)
		return
	}
	if !truthy(endpoint) {
		c.JSON(400, obj("message", "endpoint es obligatorio"))
		return
	}
	tag, err := a.db.Exec(c.ctx(), `DELETE FROM avisos_pushsuscriptions WHERE "idUser_id" = $1 AND endpoint = $2::text`, uid, dbText(endpoint))
	if err != nil {
		c.serverError(err)
		return
	}
	c.JSON(200, obj("ok", true, "deleted", tag.RowsAffected()))
}

// sendPushToUsers replica send_push_to_users (pywebpush) con webpush-go.
func (a *App) sendPushToUsers(ctx context.Context, userIDs []int64, payload OM) OM {
	data, _ := marshalJSON(payload)
	rows, err := a.db.Query(ctx, `SELECT id, endpoint, p256dh, auth FROM avisos_pushsuscriptions WHERE "idUser_id" = ANY($1)`, userIDs)
	type sub struct {
		id                   int64
		endpoint, p256, auth string
	}
	var subs []sub
	if err == nil {
		for rows.Next() {
			var s sub
			if err = rows.Scan(&s.id, &s.endpoint, &s.p256, &s.auth); err != nil {
				break
			}
			subs = append(subs, s)
		}
		rows.Close()
	}
	if err != nil {
		log.Printf("webpush: no se pudieron leer las suscripciones: %v", err)
	}

	// webpush-go antepone "mailto:" al subject; se quita para no duplicarlo
	subscriber := strings.TrimPrefix(a.cfg.WebpushSubject, "mailto:")
	client := &http.Client{Timeout: 10 * time.Second}
	sent, deleted, failed := 0, 0, 0
	var deletedIDs []int64
	for _, s := range subs {
		res, err := webpush.SendNotificationWithContext(ctx, data, &webpush.Subscription{
			Endpoint: s.endpoint,
			Keys:     webpush.Keys{P256dh: s.p256, Auth: s.auth},
		}, &webpush.Options{
			HTTPClient:      client,
			Subscriber:      subscriber,
			VAPIDPublicKey:  a.cfg.WebpushPublicKey,
			VAPIDPrivateKey: a.cfg.WebpushPrivateKey,
			TTL:             0,
		})
		if err != nil {
			log.Printf("webpush error endpoint=%s: %v", s.endpoint, err)
			failed++
			continue
		}
		res.Body.Close()
		switch {
		case res.StatusCode <= 202:
			sent++
			_, _ = a.db.Exec(ctx, `UPDATE avisos_pushsuscriptions SET last_seen_at = $1 WHERE id = $2`, nowUTC(), s.id)
		case res.StatusCode == 404 || res.StatusCode == 410:
			// La suscripción ya no existe (el usuario la quitó o cambió de navegador)
			log.Printf("webpush status %d endpoint=%s", res.StatusCode, s.endpoint)
			if _, err := a.db.Exec(ctx, `DELETE FROM avisos_pushsuscriptions WHERE id = $1`, s.id); err == nil {
				deleted++
				deletedIDs = append(deletedIDs, s.id)
			} else {
				failed++
			}
		default:
			log.Printf("webpush status %d endpoint=%s", res.StatusCode, s.endpoint)
			failed++
		}
	}
	remaining := 0
	for _, s := range subs {
		if !slices.Contains(deletedIDs, s.id) {
			remaining++
		}
	}
	return obj("subs", remaining, "sent", sent, "deleted", deleted, "failed", failed)
}
