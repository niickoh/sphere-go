// Cloudflare Worker que reenvía los envíos a Mailjet. Sirve cuando Mailjet
// bloquea la IP del servidor: la petición sale desde la red de Cloudflare.
// Variable secreta requerida: RELAY_KEY (igual a MAILJET_RELAY_KEY en la API).
export default {
  async fetch(req, env) {
    if (req.method !== "POST" || req.headers.get("X-Relay-Key") !== env.RELAY_KEY) {
      return new Response("forbidden", { status: 403 });
    }
    return fetch("https://api.mailjet.com/v3.1/send", {
      method: "POST",
      headers: {
        "Content-Type": "application/json",
        "Authorization": req.headers.get("Authorization") ?? "",
      },
      body: req.body,
    });
  },
};
