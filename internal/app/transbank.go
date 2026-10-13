package app

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"
)

const (
	pagoPendiente = "PENDING"
	pagoAprobado  = "success"
	pagoRechazado = "failed"

	// Ambiente de integración (pruebas) de Webpay Plus, igual que el SDK de Python
	webpayHost         = "https://webpay3gint.transbank.cl"
	webpayEndpoint     = "/rswebpaytransaction/api/webpay/v1.2"
	webpayCommerceCode = "597055555532"
	webpayAPIKey       = "579B532A7440BB0C9079DED94D31EA1615BACEB56610332264630D42D0A36B1C"
)

// webpayRequest replica RequestService.process_response del SDK de Transbank.
func (a *App) webpayRequest(ctx context.Context, method, path string, body any) (map[string]any, error) {
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, method, webpayHost+webpayEndpoint+path, rd)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Tbk-Api-Key-Id", webpayCommerceCode)
	req.Header.Set("Tbk-Api-Key-Secret", webpayAPIKey)
	res, err := a.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	text, err := io.ReadAll(res.Body)
	if err != nil {
		return nil, err
	}
	if len(text) == 0 {
		return nil, fmt.Errorf("respuesta vacía de Transbank (HTTP %d)", res.StatusCode)
	}
	var data map[string]any
	dec := json.NewDecoder(bytes.NewReader(text))
	dec.UseNumber()
	if err := dec.Decode(&data); err != nil {
		return nil, err
	}
	if res.StatusCode != 200 && res.StatusCode != 299 {
		msg := string(text)
		if m, ok := data["error_message"]; ok {
			msg = pyStr(m)
		} else if m, ok := data["description"]; ok {
			msg = pyStr(m)
		}
		return nil, &tbkError{msg: msg, code: res.StatusCode}
	}
	return data, nil
}

// tbkError replica str(TransactionCreateError(message, code)) del SDK: la tupla de args.
type tbkError struct {
	msg  string
	code int
}

func (e *tbkError) Error() string { return fmt.Sprintf("(%s, %d)", pyRepr(e.msg), e.code) }

// returnURLPermitida evita redirecciones a sitios ajenos: el returnUrl debe ser de un origen permitido por CORS.
func (a *App) returnURLPermitida(raw any) bool {
	s, _ := raw.(string)
	u, err := url.Parse(s)
	if err != nil {
		return false
	}
	scheme := strings.ToLower(u.Scheme)
	netloc := u.Host
	if u.User != nil {
		netloc = u.User.String() + "@" + netloc
	}
	return (scheme == "http" || scheme == "https") && slices.Contains(a.cfg.CORSAllowedOrigins, scheme+"://"+netloc)
}

// POST transbank/crear-transaccion/
func (a *App) crearTransaccion(c *Ctx) {
	data, ok := c.data()
	if !ok {
		return
	}
	returnURL := data["returnUrl"]
	if !a.returnURLPermitida(returnURL) {
		c.JSON(400, obj("message", "URL de retorno no permitida"))
		return
	}
	ctx := c.ctx()
	var suscID int64
	var planID, price, totalMeses, mesesPagados *int64
	err := a.db.QueryRow(ctx, `SELECT s.id, p.id, p.price, s."totalMeses", s."mesesPagados"
		FROM finanzas_suscripciones s LEFT JOIN finanzas_plansuscripciones p ON p.id = s."planId_id"
		WHERE s."userId_id" = $1 ORDER BY s.id LIMIT 1`, c.user.ID).Scan(&suscID, &planID, &price, &totalMeses, &mesesPagados)
	if isNoRows(err) || (err == nil && planID == nil) {
		c.JSON(404, obj("message", "No tienes una suscripción activa"))
		return
	}
	if err != nil {
		c.serverError(err)
		return
	}
	deref := func(p *int64) int64 {
		if p == nil {
			return 0
		}
		return *p
	}
	pendientes := max(deref(totalMeses)-deref(mesesPagados), 0)
	meses, ok := pyInt(data["meses"])
	if !ok {
		meses = 0
	}
	if meses < 1 || meses > pendientes {
		c.JSON(400, obj("message", fmt.Sprintf("Debes pagar entre 1 y %d meses", pendientes)))
		return
	}
	monto := *price * meses
	buyOrder := newJTI()[:26] // Transbank acepta hasta 26 caracteres
	sessionID := fmt.Sprint(c.user.ID)
	rurl := returnURL.(string)

	var resp map[string]any
	if len(rurl) > 255 {
		err = fmt.Errorf("'return_url' is too long, the maximum length is 255")
	} else {
		resp, err = a.webpayRequest(ctx, http.MethodPost, "/transactions/", map[string]string{
			"buy_order": buyOrder, "session_id": sessionID, "amount": fmt.Sprint(monto), "return_url": rurl,
		})
	}
	if err != nil {
		c.JSON(502, obj("message", "No se pudo crear la transacción", "error", err.Error()))
		return
	}
	token := pyStr(resp["token"])
	_, err = a.db.Exec(ctx, `INSERT INTO finanzas_pago (id, "suscriptionId_id", monto, moneda, status, "fechaPago", "transactionId", notas, "createdAt")
		VALUES ((SELECT COALESCE(MAX(id), 0) + 1 FROM finanzas_pago), $1, $2, 'CLP', $3, $4, $5, $6, $4)`,
		suscID, monto, pagoPendiente, nowUTC(), token, fmt.Sprintf("Pago iniciado: %d mes(es), orden %s", meses, buyOrder))
	if err != nil {
		c.serverError(err)
		return
	}
	c.JSON(200, obj("message", "Transacción creada", "token", resp["token"], "url", resp["url"], "monto", monto))
}

// POST transbank/confirmar-transaccion/
func (a *App) confirmarTransaccion(c *Ctx) {
	data, ok := c.data()
	if !ok {
		return
	}
	tokenRaw := data["token_ws"]
	if !truthy(tokenRaw) {
		c.JSON(400, obj("message", "token_ws es obligatorio"))
		return
	}
	token := pyStr(tokenRaw)
	ctx := c.ctx()
	tx, err := a.db.Begin(ctx)
	if err != nil {
		c.serverError(err)
		return
	}
	defer tx.Rollback(ctx)

	var pagoID, monto int64
	var status string
	var notas *string
	var fecha *time.Time
	var suscID, mesesPagados, totalMeses, price *int64
	err = tx.QueryRow(ctx, `SELECT p.id, p.monto, p.status, p.notas, p."fechaPago", s.id, s."mesesPagados", s."totalMeses", pl.price
		FROM finanzas_pago p
		LEFT JOIN finanzas_suscripciones s ON s.id = p."suscriptionId_id"
		LEFT JOIN finanzas_plansuscripciones pl ON pl.id = s."planId_id"
		WHERE p."transactionId" = $1 ORDER BY p.id LIMIT 1 FOR UPDATE OF p`, token).
		Scan(&pagoID, &monto, &status, &notas, &fecha, &suscID, &mesesPagados, &totalMeses, &price)
	if isNoRows(err) {
		c.JSON(404, obj("message", "Pago no encontrado"))
		return
	}
	if err != nil {
		c.serverError(err)
		return
	}
	resultado := func(resp map[string]any) OM {
		var card any
		if cd, ok := resp["card_detail"].(map[string]any); ok {
			card = cd["card_number"]
		}
		var meses any
		if suscID != nil {
			meses = ptrAny(mesesPagados)
		}
		return obj(
			"message", "Transacción confirmada",
			"status", status,
			"amount", monto,
			"transactionId", token,
			"buyOrder", resp["buy_order"],
			"authorizationCode", resp["authorization_code"],
			"paymentType", resp["payment_type_code"],
			"cardNumber", card,
			"date", pyISOPtr(fecha),
			"mesesPagados", meses,
		)
	}

	// Idempotente: si ya se confirmó (p. ej. recarga de la página), se devuelve el resultado guardado
	if status != pagoPendiente {
		_ = tx.Commit(ctx)
		c.JSON(200, resultado(map[string]any{}))
		return
	}
	notasPrev := ""
	if notas != nil {
		notasPrev = *notas
	}

	var resp map[string]any
	if len(token) > 64 {
		err = fmt.Errorf("'token' is too long, the maximum length is 64")
	} else {
		resp, err = a.webpayRequest(ctx, http.MethodPut, "/transactions/"+url.PathEscape(token), nil)
	}
	if err != nil {
		_, dbErr := tx.Exec(ctx, `UPDATE finanzas_pago SET status = $1, notas = $2 WHERE id = $3`,
			pagoRechazado, fmt.Sprintf("%s | Error al confirmar: %s", notasPrev, err), pagoID)
		if dbErr == nil {
			dbErr = tx.Commit(ctx)
		}
		if dbErr != nil {
			c.serverError(dbErr)
			return
		}
		c.JSON(502, obj("message", "No se pudo confirmar la transacción", "error", err.Error()))
		return
	}

	code, _ := intKey(resp["response_code"])
	_, isNum := resp["response_code"].(json.Number)
	aprobado := resp["status"] == "AUTHORIZED" && isNum && code == 0
	status = pagoRechazado
	texto := "Pago rechazado"
	if aprobado {
		status, texto = pagoAprobado, "Pago aprobado"
	}
	now := nowUTC()
	fecha = &now
	if _, err := tx.Exec(ctx, `UPDATE finanzas_pago SET status = $1, "fechaPago" = $2, notas = $3 WHERE id = $4`,
		status, now, fmt.Sprintf("%s | %s", notasPrev, texto), pagoID); err != nil {
		c.serverError(err)
		return
	}
	if aprobado && suscID != nil && price != nil && *price != 0 {
		var mp, tm int64
		if mesesPagados != nil {
			mp = *mesesPagados
		}
		if totalMeses != nil {
			tm = *totalMeses
		}
		meses := floorDiv(monto, *price)
		nuevo := min(mp+meses, tm)
		mesesPagados = &nuevo
		if _, err := tx.Exec(ctx, `UPDATE finanzas_suscripciones SET "mesesPagados" = $1 WHERE id = $2`, nuevo, *suscID); err != nil {
			c.serverError(err)
			return
		}
	}
	if err := tx.Commit(ctx); err != nil {
		c.serverError(err)
		return
	}
	c.JSON(200, resultado(resp))
}

// floorDiv replica el operador // de Python.
func floorDiv(a, b int64) int64 {
	q := a / b
	if (a%b != 0) && ((a < 0) != (b < 0)) {
		q--
	}
	return q
}

// ---------- reportes.views ----------

// GET reportes/dashboard/
func (a *App) dashboard(c *Ctx) {
	var u, e, p, av, ev int64
	err := a.db.QueryRow(c.ctx(), `SELECT
		(SELECT COUNT(*) FROM usuarios_usuarios),
		(SELECT COUNT(*) FROM usuarios_equipos),
		(SELECT COUNT(*) FROM finanzas_plansuscripciones),
		(SELECT COUNT(*) FROM avisos_aviso),
		(SELECT COUNT(*) FROM eventos_evento)`).Scan(&u, &e, &p, &av, &ev)
	if err != nil {
		c.serverError(err)
		return
	}
	c.JSON(200, obj(
		"message", "Datos de dashboard cargados con éxito",
		"datos", obj("totalUsuarios", u, "totalEquipos", e, "totalPlanes", p, "totalAvisos", av, "totalEventos", ev),
	))
}
