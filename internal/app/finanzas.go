package app

import (
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// ---------- finanzas.utils ----------

func pyTypeName(v any) string {
	switch t := v.(type) {
	case nil:
		return "NoneType"
	case bool:
		return "bool"
	case string:
		return "str"
	case json.Number:
		if _, err := t.Int64(); err == nil {
			return "int"
		}
		return "float"
	case []any:
		return "list"
	case map[string]any:
		return "dict"
	}
	return fmt.Sprintf("%T", v)
}

// pyRepr replica repr() de Python para strings (elige comillas como Python).
func pyRepr(v any) string {
	s, ok := v.(string)
	if !ok {
		return pyStr(v)
	}
	q := "'"
	if strings.Contains(s, "'") && !strings.Contains(s, `"`) {
		q = `"`
	}
	s = strings.ReplaceAll(s, `\`, `\\`)
	if q == "'" {
		s = strings.ReplaceAll(s, "'", `\'`)
	}
	s = strings.NewReplacer("\n", `\n`, "\r", `\r`, "\t", `\t`).Replace(s)
	return q + s + q
}

// parseISO replica datetime.fromisoformat (Python 3.12) para los formatos habituales.
func parseISO(s string) (time.Time, bool) {
	if len(s) > 10 && s[10] == ' ' {
		s = s[:10] + "T" + s[11:]
	}
	layouts := []string{
		"2006-01-02",
		"2006-01-02T15", "2006-01-02T15:04", "2006-01-02T15:04:05", "2006-01-02T15:04:05.999999999",
		"2006-01-02T15Z07:00", "2006-01-02T15:04Z07:00", "2006-01-02T15:04:05Z07:00", "2006-01-02T15:04:05.999999999Z07:00",
		"2006-01-02T15:04-0700", "2006-01-02T15:04:05-0700", "2006-01-02T15:04:05.999999999-0700",
		"2006-01-02T15:04:05-07", "2006-01-02T15:04:05.999999999-07",
		"20060102", "20060102T150405",
	}
	for _, l := range layouts {
		if t, err := time.Parse(l, s); err == nil {
			return t, true
		}
	}
	return time.Time{}, false
}

// addMonthsSafeUTC replica add_months_safe_utc: 31-ene + 1 mes = 28/29-feb.
func addMonthsSafeUTC(d time.Time, months int) time.Time {
	total := int(d.Month()) + months - 1
	y := d.Year() + total/12
	m := total%12 + 1
	last := time.Date(y, time.Month(m)+1, 0, 0, 0, 0, 0, time.UTC).Day()
	day := min(d.Day(), last)
	return time.Date(y, time.Month(m), day, d.Hour(), d.Minute(), d.Second(), d.Nanosecond(), time.UTC)
}

// countMonthlyOccurrences replica finanzas.utils.count_monthly_occurrences(start, end).
// Devuelve error con el mismo texto que la excepción de Python cuando el valor no es string.
func countMonthlyOccurrences(start, end any) (int, error) {
	var startDT, endDT time.Time
	if s, ok := start.(string); ok {
		t, ok := parseISO(strings.ReplaceAll(s, "Z", "+00:00"))
		if !ok {
			return 0, nil
		}
		startDT = t
	}
	if s, ok := end.(string); ok {
		t, ok := parseISO(strings.ReplaceAll(s, "Z", "+00:00"))
		if !ok {
			return 0, nil
		}
		endDT = t
	}
	if _, ok := start.(string); !ok {
		return 0, fmt.Errorf("'%s' object has no attribute 'tzinfo'", pyTypeName(start))
	}
	if _, ok := end.(string); !ok {
		return 0, fmt.Errorf("'%s' object has no attribute 'tzinfo'", pyTypeName(end))
	}
	if endDT.Before(startDT) {
		return 0, nil
	}
	// El cursor toma la hora "de pared" del inicio y la marca como UTC (igual que en Python)
	cursor := time.Date(startDT.Year(), startDT.Month(), startDT.Day(), startDT.Hour(), startDT.Minute(),
		startDT.Second(), startDT.Nanosecond(), time.UTC)
	cursor = addMonthsSafeUTC(cursor, 1)
	count := 0
	for !cursor.After(endDT) {
		count++
		next := addMonthsSafeUTC(cursor, 1)
		if next.Equal(cursor) {
			break
		}
		cursor = next
		if count > 1000 {
			break
		}
	}
	return count, nil
}

// ---------- finanzas.views: planes ----------

func errInesperado(c *Ctx, err error) {
	c.JSON(500, obj("message", "Ocurrió un error inesperado", "detail", pgErrDetail(err)))
}

func pgErrDetail(err error) string {
	return strings.TrimSuffix(pgErrText(err), "\n")
}

// POST finanzas/agregar/
func (a *App) addSuscriptionsPlans(c *Ctx) {
	body, err := c.loadsObj()
	if errors.Is(err, errNotObject) {
		c.serverError(err)
		return
	}
	if err != nil {
		c.JSON(500, obj("message", "Ocurrio un error inesperado"))
		return
	}
	name, description, price := body["name"], body["description"], body["price"]
	interval, intervalCount, active := body["interval"], body["intervalCount"], body["active"]
	if !truthy(name) || !truthy(description) || !truthy(price) || !truthy(interval) || !truthy(intervalCount) {
		c.JSON(400, obj("message", "Todos los campos son obligatorios"))
		return
	}
	var id int64
	err = a.db.QueryRow(c.ctx(), `INSERT INTO finanzas_plansuscripciones (id, name, description, price, "interval", "intervalCount", active, "createdAt")
		VALUES ((SELECT COALESCE(MAX(id), 0) + 1 FROM finanzas_plansuscripciones), $1::text, $2::text, $3::text::int, $4::text, $5::text::int, $6::text::boolean, $7)
		RETURNING id`, dbText(name), dbText(description), dbText(price), dbText(interval), dbText(intervalCount), dbText(active), nowUTC()).Scan(&id)
	if err != nil {
		if isIntegrityErr(err) {
			if strings.Contains(pgErrText(err), "name") {
				c.JSON(400, obj("error", fmt.Sprintf("El nombre '%s' ya está registrado", pyStr(name))))
				return
			}
			// En Django este caso terminaba en UnboundLocalError (500)
			c.serverError(err)
			return
		}
		c.JSON(500, obj("error", "Error inesperado: "+pgErrText(err)))
		return
	}
	c.JSON(200, obj(
		"message", "Plan de suscripción creado con éxito",
		"planSuscripcion", obj("id", id, "name", name, "description", description, "price", price,
			"interval", interval, "intervalCount", intervalCount, "active", active),
	))
}

// GET finanzas/listar/
func (a *App) listSuscriptionPlans(c *Ctx) {
	rows, err := a.db.Query(c.ctx(), `SELECT id, name, description, price, "interval", "intervalCount", active FROM finanzas_plansuscripciones`)
	if err != nil {
		c.serverError(err)
		return
	}
	defer rows.Close()
	data := []OM{}
	for rows.Next() {
		var id, price, intervalCount int64
		var name, description, interval string
		var active bool
		if err := rows.Scan(&id, &name, &description, &price, &interval, &intervalCount, &active); err != nil {
			c.serverError(err)
			return
		}
		data = append(data, obj("id", id, "name", name, "description", description, "price", price,
			"interval", interval, "intervalCount", intervalCount, "active", active))
	}
	if err := rows.Err(); err != nil {
		c.serverError(err)
		return
	}
	c.JSON(200, data)
}

// PUT finanzas/editar/<int:id>
func (a *App) editSuscriptionPlans(c *Ctx) {
	id := c.intParam("id")
	var name, description, interval string
	var price, intervalCount int64
	var active bool
	err := a.db.QueryRow(c.ctx(), `SELECT name, description, price, "interval", "intervalCount", active
		FROM finanzas_plansuscripciones WHERE id = $1`, id).Scan(&name, &description, &price, &interval, &intervalCount, &active)
	if isNoRows(err) {
		c.JSON(404, obj("error", "Plan de suscripción no encontrado"))
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
		c.JSON(500, obj("message", "Ocurrio un error inesperado"))
		return
	}
	vals := map[string]any{"name": name, "description": description, "price": price, "interval": interval,
		"intervalCount": intervalCount, "active": active}
	for k := range vals {
		if v, ok := body[k]; ok {
			vals[k] = v
		}
	}
	_, err = a.db.Exec(c.ctx(), `UPDATE finanzas_plansuscripciones SET name = $1::text, description = $2::text, price = $3::text::int,
		"interval" = $4::text, "intervalCount" = $5::text::int, active = $6::text::boolean WHERE id = $7`,
		dbText(vals["name"]), dbText(vals["description"]), dbTextAny(vals["price"]), dbText(vals["interval"]),
		dbTextAny(vals["intervalCount"]), dbTextAny(vals["active"]), id)
	if err != nil {
		c.serverError(err)
		return
	}
	c.JSON(200, obj(
		"message", "Plan de suscripcion actualizado con éxito",
		"planSuscripcion", obj("id", id, "name", vals["name"], "description", vals["description"], "price", vals["price"],
			"interval", vals["interval"], "intervalCount", vals["intervalCount"], "active", vals["active"]),
	))
}

// dbTextAny acepta además los tipos nativos leídos de la BD.
func dbTextAny(v any) any {
	switch t := v.(type) {
	case int64:
		return fmt.Sprint(t)
	case bool:
		if t {
			return "true"
		}
		return "false"
	}
	return dbText(v)
}

// DELETE finanzas/eliminar/<int:id>
func (a *App) deleteSuscriptionPlans(c *Ctx) {
	ok, err := a.deleteByID(c.ctx(), "finanzas_plansuscripciones", c.intParam("id"))
	if err != nil {
		c.serverError(err)
		return
	}
	if !ok {
		c.JSON(404, obj("error", "Plan de suscripción no encontrado"))
		return
	}
	c.JSON(200, obj("message", "Plan de suscripcion eliminado con éxito"))
}

// ---------- finanzas.views: suscripciones ----------

type nuevaSuscripcion struct {
	userID, planID                       int64
	status, start, end, next, canceledAt any
	totalMeses                           int
}

// insertSuscripciones replica Suscripciones.objects.bulk_create (usa la secuencia, no MAX+1).
func (a *App) insertSuscripciones(c *Ctx, subs []nuevaSuscripcion) error {
	if len(subs) == 0 {
		return nil
	}
	ctx := c.ctx()
	tx, err := a.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	now := nowUTC()
	for _, s := range subs {
		_, err := tx.Exec(ctx, `INSERT INTO finanzas_suscripciones ("userId_id", "planId_id", status, "startDate", "endDate",
			"nextBillingDate", "canceledAt", "createdAt", "totalMeses", "mesesPagados")
			VALUES ($1, $2, $3::text, $4::text::timestamptz, $5::text::timestamptz, $6::text::timestamptz, $7::text::timestamptz, $8, $9, 0)`,
			s.userID, s.planID, dbText(s.status), dbText(s.start), dbText(s.end), dbText(s.next), dbText(s.canceledAt), now, s.totalMeses)
		if err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

// POST finanzas/asignarPlan/
func (a *App) asignPlanUsers(c *Ctx) {
	v, err := c.loads()
	if err != nil {
		c.JSON(400, obj("message", "JSON inválido"))
		return
	}
	body, ok := v.(map[string]any)
	if !ok {
		errInesperado(c, fmt.Errorf("'%s' object has no attribute 'get'", pyTypeName(v)))
		return
	}
	planID := body["planId"]
	canceledAt := body["canceledAt"]
	if !truthy(canceledAt) {
		canceledAt = nil
	}
	totalMeses, err := countMonthlyOccurrences(body["startDate"], body["endDate"])
	if err != nil {
		errInesperado(c, err)
		return
	}

	ctx := c.ctx()
	rows, err := a.db.Query(ctx, `SELECT id FROM usuarios_usuarios WHERE tipo_id = 3`)
	if err != nil {
		errInesperado(c, err)
		return
	}
	atletas, err := pgx.CollectRows(rows, pgx.RowTo[int64])
	if err != nil {
		errInesperado(c, err)
		return
	}

	var subs []nuevaSuscripcion
	if len(atletas) > 0 {
		var plan int64
		err := pgx.ErrNoRows
		if planID != nil {
			err = a.db.QueryRow(ctx, `SELECT id FROM finanzas_plansuscripciones WHERE id = $1::text::int`, dbText(planID)).Scan(&plan)
		}
		if isNoRows(err) {
			c.JSON(400, obj("message", "Plan no encontrado"))
			return
		}
		if err != nil {
			errInesperado(c, err)
			return
		}
		for _, uid := range atletas {
			subs = append(subs, nuevaSuscripcion{userID: uid, planID: plan, status: body["status"], start: body["startDate"],
				end: body["endDate"], next: body["nextBillingDate"], canceledAt: canceledAt, totalMeses: totalMeses})
		}
	}
	if err := a.insertSuscripciones(c, subs); err != nil {
		errInesperado(c, err)
		return
	}
	c.JSON(201, obj("message", "Suscripción(es) creada(s) con éxito"))
}

// POST finanzas/asignarPlanesUser/
func (a *App) asignarPlanesToUser(c *Ctx) {
	v, err := c.loads()
	if err != nil {
		c.JSON(400, obj("message", "JSON inválido"))
		return
	}
	body, ok := v.(map[string]any)
	if !ok {
		errInesperado(c, fmt.Errorf("'%s' object has no attribute 'get'", pyTypeName(v)))
		return
	}
	defPlan := body["planId"]
	defStatus, defStart, defEnd, defNext := body["status"], body["startDate"], body["endDate"], body["nextBillingDate"]
	defCanceled := body["canceledAt"]
	if !truthy(defCanceled) {
		defCanceled = nil
	}

	items, ok := body["suscripciones"].([]any)
	if _, present := body["suscripciones"]; !present {
		items, ok = []any{}, true
	}
	if !ok || len(items) == 0 {
		c.JSON(400, obj("message", "suscripciones debe ser una lista no vacía"))
		return
	}
	var dicts []map[string]any
	var userIDs []int64
	for _, it := range items {
		m, ok := it.(map[string]any)
		if !ok {
			errInesperado(c, fmt.Errorf("'%s' object has no attribute 'get'", pyTypeName(it)))
			return
		}
		dicts = append(dicts, m)
	}
	count := 0
	for _, m := range dicts {
		if m["userId"] != nil {
			count++
		}
	}
	if count != len(dicts) {
		c.JSON(400, obj("message", "Cada item en suscripciones debe incluir idUsuario"))
		return
	}
	var planIDs []int64
	collectPlan := func(p any) bool {
		n, ok := pyInt(p)
		if !ok {
			errInesperado(c, fmt.Errorf("Field 'id' expected a number but got %s.", pyRepr(p)))
			return false
		}
		planIDs = append(planIDs, n)
		return true
	}
	hasPlan := false
	if truthy(defPlan) {
		hasPlan = true
		if !collectPlan(defPlan) {
			return
		}
	}
	for _, m := range dicts {
		if truthy(m["planId"]) {
			hasPlan = true
			if !collectPlan(m["planId"]) {
				return
			}
		}
	}
	if !hasPlan {
		c.JSON(400, obj("message", "Debes enviar planId global o planId en cada item"))
		return
	}
	for _, m := range dicts {
		n, ok := pyInt(m["userId"])
		if !ok {
			errInesperado(c, fmt.Errorf("Field 'id' expected a number but got %s.", pyRepr(m["userId"])))
			return
		}
		userIDs = append(userIDs, n)
	}

	ctx := c.ctx()
	existing := func(table string, ids []int64) ([]int64, error) {
		rows, err := a.db.Query(ctx, `SELECT id FROM `+table+` WHERE id = ANY($1)`, ids)
		if err != nil {
			return nil, err
		}
		return pgx.CollectRows(rows, pgx.RowTo[int64])
	}
	users, err := existing("usuarios_usuarios", userIDs)
	if err != nil {
		errInesperado(c, err)
		return
	}
	plans, err := existing("finanzas_plansuscripciones", planIDs)
	if err != nil {
		errInesperado(c, err)
		return
	}

	or := func(x, def any) any {
		if truthy(x) {
			return x
		}
		return def
	}
	var subs []nuevaSuscripcion
	for _, m := range dicts {
		uidRaw := m["userId"]
		uid, ok := intKey(uidRaw)
		if !ok || !slices.Contains(users, uid) {
			c.JSON(400, obj("message", fmt.Sprintf("Usuario no encontrado: %s", pyStr(uidRaw))))
			return
		}
		pidRaw := or(m["planId"], defPlan)
		pid, ok := intKey(pidRaw)
		if !ok || !slices.Contains(plans, pid) {
			c.JSON(400, obj("message", fmt.Sprintf("Plan no encontrado: %s", pyStr(pidRaw))))
			return
		}
		s := nuevaSuscripcion{userID: uid, planID: pid,
			status: or(m["status"], defStatus), start: or(m["startDate"], defStart),
			end: or(m["endDate"], defEnd), next: or(m["nextBillingDate"], defNext),
			canceledAt: or(m["canceledAt"], defCanceled)}
		var missing []string
		for _, f := range []struct {
			k string
			v any
		}{{"status", s.status}, {"startDate", s.start}, {"endDate", s.end}, {"nextBillingDate", s.next}} {
			if !truthy(f.v) {
				missing = append(missing, f.k)
			}
		}
		if len(missing) > 0 {
			c.JSON(400, obj("message", fmt.Sprintf("Faltan campos en suscripción de usuario %s", pyStr(uidRaw)), "missing", missing))
			return
		}
		if s.totalMeses, err = countMonthlyOccurrences(s.start, s.end); err != nil {
			errInesperado(c, err)
			return
		}
		subs = append(subs, s)
	}
	if err := a.insertSuscripciones(c, subs); err != nil {
		errInesperado(c, err)
		return
	}
	c.JSON(201, obj("message", "Suscripciones creadas con éxito", "created", len(subs)))
}

// GET finanzas/getSuscription/<int:userId>
func (a *App) getSuscripcionByUser(c *Ctx) {
	userID := c.intParam("userId")
	if !isSelfOrStaff(c.user, userID) {
		c.JSON(403, obj("message", "No tienes permisos para ver esta suscripción"))
		return
	}
	rows, err := a.db.Query(c.ctx(), `SELECT s.id, s."userId_id", s."planId_id", s.status, s."startDate", s."endDate",
		s."nextBillingDate", s."canceledAt", s."createdAt", s."totalMeses", s."mesesPagados",
		p.id, p.name, p.description, p.price, p."interval", p."intervalCount", p.active, p."createdAt"
		FROM finanzas_suscripciones s
		JOIN usuarios_usuarios u ON u.id = s."userId_id"
		LEFT JOIN finanzas_plansuscripciones p ON p.id = s."planId_id"
		WHERE u.id = $1 LIMIT 21`, userID)
	if err != nil {
		errInesperado(c, err)
		return
	}
	defer rows.Close()

	type susc struct {
		id                       int64
		userID, planID           *int64
		status                   string
		start, end               time.Time
		next, canceled, created  *time.Time
		totalMeses, mesesPagados *int64
		pID, pPrice, pCount      *int64
		pName, pDesc, pInterval  *string
		pActive                  *bool
		pCreated                 *time.Time
	}
	var found []susc
	for rows.Next() {
		var s susc
		if err := rows.Scan(&s.id, &s.userID, &s.planID, &s.status, &s.start, &s.end, &s.next, &s.canceled, &s.created,
			&s.totalMeses, &s.mesesPagados, &s.pID, &s.pName, &s.pDesc, &s.pPrice, &s.pInterval, &s.pCount, &s.pActive, &s.pCreated); err != nil {
			errInesperado(c, err)
			return
		}
		found = append(found, s)
	}
	if err := rows.Err(); err != nil {
		errInesperado(c, err)
		return
	}
	switch {
	case len(found) == 0:
		c.JSON(200, obj("message", fmt.Sprintf("Suscripción del usuario con id => %d no encontrada", userID)))
		return
	case len(found) > 1:
		n := fmt.Sprint(len(found))
		if len(found) > 20 {
			n = "more than 20"
		}
		c.JSON(500, obj("message", "Ocurrió un error inesperado",
			"detail", fmt.Sprintf("get() returned more than one Suscripciones -- it returned %s!", n)))
		return
	}
	s := found[0]
	var planData any
	if s.pID != nil {
		planData = obj("id", *s.pID, "name", ptrAny(s.pName), "description", ptrAny(s.pDesc), "price", ptrAny(s.pPrice),
			"interval", ptrAny(s.pInterval), "intervalCount", ptrAny(s.pCount), "active", ptrAny(s.pActive),
			"createdAt", pyISOPtr(s.pCreated))
	}
	var userData any
	if s.userID != nil {
		userData = obj("id", *s.userID)
	}
	c.JSON(200, obj(
		"message", "Suscripción encontrada con éxito",
		"suscripcion", obj(
			"id", s.id,
			"userId", ptrAny(s.userID),
			"user", userData,
			"planId", ptrAny(s.pID),
			"plan", planData,
			"status", s.status,
			"startDate", pyISO(s.start),
			"endDate", pyISO(s.end),
			"nextBillingDate", pyISOPtr(s.next),
			"canceledAt", pyISOPtr(s.canceled),
			"createdAt", pyISOPtr(s.created),
			"totalMeses", ptrAny(s.totalMeses),
			"mesesPagados", ptrAny(s.mesesPagados),
		),
	))
}

// ---------- finanzas.views: pagos ----------

// POST finanzas/addPago/
func (a *App) addPayment(c *Ctx) {
	body, err := c.loadsObj()
	if errors.Is(err, errNotObject) {
		c.serverError(err)
		return
	}
	if err != nil {
		c.JSON(500, obj("message", "Ocurrio un error inesperado"))
		return
	}
	suscriptionID, monto, moneda := body["suscriptionId"], body["monto"], body["moneda"]
	estado, fechaPago, transactionID, notas := body["status"], body["fechaPago"], body["transactionId"], body["notas"]
	if !truthy(suscriptionID) || !truthy(monto) || !truthy(moneda) || !truthy(estado) || !truthy(fechaPago) || !truthy(transactionID) {
		c.JSON(400, obj("message", "Todos los campos son obligatorios"))
		return
	}
	ctx := c.ctx()
	var suscPK int64
	err = a.db.QueryRow(ctx, `SELECT id FROM finanzas_suscripciones WHERE id = $1::text::int`, dbText(suscriptionID)).Scan(&suscPK)
	if isNoRows(err) {
		c.JSON(500, obj("error", "Error inesperado: Suscripciones matching query does not exist."))
		return
	}
	var id int64
	if err == nil {
		err = a.db.QueryRow(ctx, `INSERT INTO finanzas_pago (id, "suscriptionId_id", monto, moneda, status, "fechaPago", "transactionId", notas, "createdAt")
			VALUES ((SELECT COALESCE(MAX(id), 0) + 1 FROM finanzas_pago), $1, $2::text::int, $3::text, $4::text, $5::text::timestamptz, $6::text, $7::text, $8)
			RETURNING id`, suscPK, dbText(monto), dbText(moneda), dbText(estado), dbText(fechaPago), dbText(transactionID), dbText(notas), nowUTC()).Scan(&id)
	}
	if err != nil {
		c.JSON(500, obj("error", "Error inesperado: "+pgErrText(err)))
		return
	}
	c.JSON(200, obj(
		"message", "Pago ingresado con éxito",
		"pago", obj("id", id, "suscriptionId", suscPK, "monto", monto, "moneda", moneda, "status", estado,
			"fechaPago", fechaPago, "transactionId", transactionID, "notas", notas),
	))
}

// PUT finanzas/editPago/<str:id>
func (a *App) editPayment(c *Ctx) {
	trxID := c.params["id"]
	ctx := c.ctx()
	rows, err := a.db.Query(ctx, `SELECT id, "suscriptionId_id", monto, moneda, status, "fechaPago", "transactionId", notas
		FROM finanzas_pago WHERE "transactionId" = $1 LIMIT 21`, trxID)
	if err != nil {
		c.serverError(err)
		return
	}
	type pago struct {
		id                  int64
		suscID              *int64
		monto               int64
		moneda, status, trx string
		fecha               *time.Time
		notas               *string
	}
	var found []pago
	for rows.Next() {
		var p pago
		if err := rows.Scan(&p.id, &p.suscID, &p.monto, &p.moneda, &p.status, &p.fecha, &p.trx, &p.notas); err != nil {
			rows.Close()
			c.serverError(err)
			return
		}
		found = append(found, p)
	}
	rows.Close()
	if len(found) == 0 {
		c.JSON(404, obj("error", "Pago no encontrado"))
		return
	}
	if len(found) > 1 {
		c.serverError(fmt.Errorf("get() returned more than one Pago -- it returned %d!", len(found)))
		return
	}
	p := found[0]
	body, err := c.loadsObj()
	if err != nil {
		c.serverError(err)
		return
	}

	var monto, moneda, status, notas any = p.monto, p.moneda, p.status, ptrAny(p.notas)
	fecha := drfISOPtr(p.fecha)
	sets := []string{}
	args := []any{}
	set := func(expr string, v any) {
		args = append(args, v)
		sets = append(sets, fmt.Sprintf(expr, len(args)))
	}
	if v, ok := body["monto"]; ok {
		monto = v
		set(`monto = $%d::text::int`, dbText(v))
	}
	if v, ok := body["moneda"]; ok {
		moneda = v
		set(`moneda = $%d::text`, dbText(v))
	}
	if v, ok := body["status"]; ok {
		status = v
		set(`status = $%d::text`, dbText(v))
	}
	if v, ok := body["fechaPago"]; ok {
		fecha = v
		set(`"fechaPago" = $%d::text::timestamptz`, dbText(v))
	}
	suscID := p.suscID
	if v, ok := body["suscriptionId"]; ok {
		var sid int64
		err := pgx.ErrNoRows
		if v != nil {
			err = a.db.QueryRow(ctx, `SELECT id FROM finanzas_suscripciones WHERE id = $1::text::int`, dbText(v)).Scan(&sid)
		}
		if err != nil {
			c.serverError(err)
			return
		}
		suscID = &sid
		set(`"suscriptionId_id" = $%d`, sid)
	}
	if v, ok := body["notas"]; ok {
		notas = v
		set(`notas = $%d::text`, dbText(v))
	}

	// suscripcion = Suscripciones.objects.get(pk=payment.suscriptionId_id)
	if suscID == nil {
		c.serverError(errors.New("Suscripciones matching query does not exist."))
		return
	}
	var exists bool
	if err := a.db.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM finanzas_suscripciones WHERE id = $1)`, *suscID).Scan(&exists); err != nil || !exists {
		c.serverError(fmt.Errorf("Suscripciones matching query does not exist. (%v)", err))
		return
	}

	tx, err := a.db.Begin(ctx)
	if err != nil {
		c.serverError(err)
		return
	}
	defer tx.Rollback(ctx)
	if v, ok := body["mesesPagados"]; ok {
		if _, err := tx.Exec(ctx, `UPDATE finanzas_suscripciones SET "mesesPagados" = $1::text::int WHERE id = $2`, dbText(v), *suscID); err != nil {
			c.serverError(err)
			return
		}
	}
	if len(sets) > 0 {
		args = append(args, p.id)
		q := fmt.Sprintf(`UPDATE finanzas_pago SET %s WHERE id = $%d`, strings.Join(sets, ", "), len(args))
		if _, err := tx.Exec(ctx, q, args...); err != nil {
			c.serverError(err)
			return
		}
	}
	if err := tx.Commit(ctx); err != nil {
		c.serverError(err)
		return
	}
	c.JSON(200, obj(
		"message", "Pago actualizado con éxito",
		"pago", obj("id", p.id, "suscriptionId", ptrAny(suscID), "monto", monto, "moneda", moneda, "status", status,
			"fechaPago", fecha, "transactionId", p.trx, "notas", notas),
	))
}
