package app

// registerRoutes replica mysite/urls.py y los urls.py de cada app, con los mismos
// métodos y permission_classes de cada vista.
func (a *App) registerRoutes() {
	GET, POST, PUT, DELETE := []string{"GET"}, []string{"POST"}, []string{"PUT"}, []string{"DELETE"}

	// mysite/urls.py
	a.handle("auth/login/", POST, AllowAny, a.login)
	a.handle("auth/logout/", POST, AllowAny, a.logout)
	a.handle("auth/refresh/", POST, AllowAny, a.refreshTokenView)
	a.handle("equipos/add/", POST, IsClubAdmin, a.addTeam)
	a.handle("equipos/listar/", GET, IsAuthenticated, a.listarEquipos)
	a.handle("equipos/edit/<int:id>", PUT, IsClubAdmin, a.editTeam)
	a.handle("equipos/delete/<int:id>", DELETE, IsClubAdmin, a.deleteTeam)

	// usuarios/urls.py
	a.handle("usuarios/listar/", GET, IsClubStaff, a.listarUsuarios)
	a.handle("usuarios/agregar/", POST, IsClubAdmin, a.addUsuario)
	a.handle("usuarios/eliminar/<int:id>", DELETE, IsClubAdmin, a.deleteUsuario)
	a.handle("usuarios/editar/<int:id>", PUT, IsAuthenticated, a.editUsuario)
	a.handle("usuarios/me/", GET, IsAuthenticated, a.me)
	a.handle("usuarios/datosMedicos/<int:idUser>", []string{"PUT", "PATCH"}, IsAuthenticated, a.datosMedicosUpdate)
	a.handle("usuarios/getDatosMedicos/<int:idUser>", GET, IsAuthenticated, a.datosMedicosGet)

	// apitransbank/urls.py
	a.handle("transbank/crear-transaccion/", POST, IsAuthenticated, a.crearTransaccion)
	a.handle("transbank/confirmar-transaccion/", POST, AllowAny, a.confirmarTransaccion)

	// finanzas/urls.py
	a.handle("finanzas/listar/", GET, IsAuthenticated, a.listSuscriptionPlans)
	a.handle("finanzas/agregar/", POST, IsClubAdmin, a.addSuscriptionsPlans)
	a.handle("finanzas/eliminar/<int:id>", DELETE, IsClubAdmin, a.deleteSuscriptionPlans)
	a.handle("finanzas/editar/<int:id>", PUT, IsClubAdmin, a.editSuscriptionPlans)
	a.handle("finanzas/asignarPlan/", POST, IsClubAdmin, a.asignPlanUsers)
	a.handle("finanzas/asignarPlanesUser/", POST, IsClubAdmin, a.asignarPlanesToUser)
	a.handle("finanzas/getSuscription/<int:userId>", GET, IsAuthenticated, a.getSuscripcionByUser)
	a.handle("finanzas/addPago/", POST, IsClubAdmin, a.addPayment)
	a.handle("finanzas/editPago/<str:id>", PUT, IsClubAdmin, a.editPayment)

	// reportes/urls.py
	a.handle("reportes/dashboard/", GET, IsClubStaff, a.dashboard)

	// sendMail/urls.py
	a.handle("apiMail/confirmacionPago/", POST, IsClubAdmin, a.confirmacionPago)
	a.handle("apiMail/bienvenida/", POST, IsClubAdmin, a.bienvenida)
	a.handle("apiMail/bienvenidaNew/", POST, IsClubAdmin, a.bienvenidaNew)
	a.handle("apiMail/contacto/", POST, AllowAny, a.contacto)
	a.handle("apiMail/contacto-upac/", POST, AllowAny, a.contactoUpac)

	// equipos/urls.py
	a.handle("equipos/asignarEquipo/", POST, IsClubAdmin, a.asignarEquipo)
	a.handle("equipos/quitarEquipo/", POST, IsClubAdmin, a.quitarEquipo)
	a.handle("equipos/listAsignadosEquipo/<int:idTeam>", GET, IsAuthenticated, a.verAsignadosEquipo)
	a.handle("equipos/getTeamsByUser/<int:user_id>", GET, IsAuthenticated, a.obtenerEquiposByUser)

	// eventos/urls.py (get_team_events no declaraba permisos: usaba el IsAuthenticated por defecto)
	a.handle("eventos/getUsersEvents/<int:team_id>", GET, IsAuthenticated, a.getTeamEvents)
	a.handle("eventos/crearEvento/", POST, IsClubAdmin, a.createEvent)
	a.handle("eventos/listEventos/", GET, IsAuthenticated, a.getEvents)
	a.handle("eventos/deleteEvento/<int:id>", DELETE, IsClubAdmin, a.deleteEvento)

	// avisos/urls.py
	a.handle("avisos/listar/", GET, IsAuthenticated, a.listarAvisos)
	a.handle("avisos/add/", POST, IsClubAdmin, a.addAviso)
	a.handle("avisos/edit/<int:id>", PUT, IsClubAdmin, a.editAviso)
	a.handle("avisos/delete/<int:id>", DELETE, IsClubAdmin, a.deleteAviso)
	a.handle("avisos/mis-avisos/<int:user>/<str:unread>", GET, IsAuthenticated, a.misAvisos)
	a.handle("avisos/unread-count/<int:user>", GET, IsAuthenticated, a.unreadCount)
	a.handle("avisos/read/<int:aviso_id>", POST, IsAuthenticated, a.marcarLeido)
	a.handle("avisos/read-all/", POST, IsAuthenticated, a.marcarTodosLeidos)
	a.handle("avisos/push/subscribe/", POST, IsAuthenticated, a.pushSubscribe)
	a.handle("avisos/push/unsubscribe/", DELETE, IsAuthenticated, a.pushUnsubscribe)
}
