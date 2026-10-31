package controller

import (
	"context"
	"net/http"
	"strconv"
	"time"

	"github.com/mhsanaei/3x-ui/v3/internal/web/middleware"
	"github.com/mhsanaei/3x-ui/v3/internal/web/service"
	"github.com/mhsanaei/3x-ui/v3/internal/web/service/integration"

	"github.com/gin-gonic/gin"
)

// The Psiphon routes run this panel's own exit countries. They sit under
// /relays, so they are super-admin only as well.

type psiphonCodesRequest struct {
	Codes []string `json:"codes"`
}

// psiphonTrafficResetRequest names the country to reset, or sets all for every country.
type psiphonTrafficResetRequest struct {
	Code string `json:"code"`
	All  bool   `json:"all"`
}

type psiphonWarpRequest struct {
	Mode  string `json:"mode"`
	Sites string `json:"sites"`
}

// psiphonSlowLimit covers the 36 MB download and the two-start country probe.
const psiphonSlowLimit = 3 * time.Minute

func (a *RelayController) initPsiphonRouter(g *gin.RouterGroup) {
	g.GET("/psiphon/state", a.psiphonState)
	g.POST("/psiphon/install", a.psiphonInstall)
	g.POST("/psiphon/regions/refresh", a.psiphonRefreshRegions)
	g.POST("/psiphon/countries", a.psiphonCountries)
	g.GET("/psiphon/inbounds", a.psiphonInbounds)
	g.POST("/psiphon/inbound/:id", a.psiphonInbound)
	g.POST("/psiphon/warp", a.psiphonWarp)
	g.POST("/psiphon/traffic/reset", a.psiphonResetTraffic)
	g.POST("/psiphon/test", a.psiphonTest)
	g.POST("/psiphon/scan", a.psiphonScan)
}

// psiphonWriteLimit lets an action outlive the server's 30-second write limit;
// where the writer cannot be extended the action is held inside that limit instead.
func psiphonWriteLimit(c *gin.Context, limit time.Duration) time.Duration {
	if err := http.NewResponseController(c.Writer).SetWriteDeadline(time.Now().Add(limit + 15*time.Second)); err != nil {
		return 25 * time.Second
	}
	return limit
}

func (a *RelayController) psiphonState(c *gin.Context) {
	state, err := a.psiphonService.State()
	if err != nil {
		jsonMsg(c, I18nWeb(c, "pages.relays.psiphon.toasts.state"), err)
		return
	}
	jsonObj(c, state, nil)
}

// psiphonInstall answers once the client is fetched and its countries read; the
// work is not tied to the request, so a closed page does not cut a download short.
func (a *RelayController) psiphonInstall(c *gin.Context) {
	ctx, cancel := context.WithTimeout(context.Background(), psiphonWriteLimit(c, psiphonSlowLimit))
	defer cancel()
	jsonMsg(c, I18nWeb(c, "pages.relays.psiphon.toasts.install"), a.psiphonService.Install(ctx))
}

func (a *RelayController) psiphonRefreshRegions(c *gin.Context) {
	ctx, cancel := context.WithTimeout(context.Background(), psiphonWriteLimit(c, psiphonSlowLimit))
	defer cancel()
	jsonMsg(c, I18nWeb(c, "pages.relays.psiphon.toasts.regions"), a.psiphonService.RefreshRegions(ctx))
}

func (a *RelayController) psiphonCountries(c *gin.Context) {
	req, ok := middleware.BindJSONAndValidate[psiphonCodesRequest](c)
	if !ok {
		return
	}
	jsonMsg(c, I18nWeb(c, "pages.relays.psiphon.toasts.countries"), a.psiphonService.SetCountries(req.Codes))
}

func (a *RelayController) psiphonInbounds(c *gin.Context) {
	rows, err := a.psiphonService.InboundChoices()
	if err != nil {
		jsonMsg(c, I18nWeb(c, "pages.relays.psiphon.toasts.inbounds"), err)
		return
	}
	jsonObj(c, rows, nil)
}

func (a *RelayController) psiphonInbound(c *gin.Context) {
	msg := I18nWeb(c, "pages.relays.psiphon.toasts.inbound")
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		jsonMsg(c, msg, err)
		return
	}
	req, ok := middleware.BindJSONAndValidate[psiphonCodesRequest](c)
	if !ok {
		return
	}
	jsonMsg(c, msg, a.psiphonService.SetInboundCountries(id, req.Codes))
}

// psiphonWarp may enroll a WARP device first: two calls to Cloudflare, fifteen
// seconds each at most.
func (a *RelayController) psiphonWarp(c *gin.Context) {
	req, ok := middleware.BindJSONAndValidate[psiphonWarpRequest](c)
	if !ok {
		return
	}
	psiphonWriteLimit(c, time.Minute)
	warp := &integration.WarpService{}
	jsonMsg(c, I18nWeb(c, "pages.relays.psiphon.toasts.warp"), a.psiphonService.SetWarp(req.Mode, req.Sites, warp.RegWarpMasque))
}

func (a *RelayController) psiphonResetTraffic(c *gin.Context) {
	req, ok := middleware.BindJSONAndValidate[psiphonTrafficResetRequest](c)
	if !ok {
		return
	}
	jsonMsg(c, I18nWeb(c, "pages.relays.psiphon.toasts.traffic"), a.psiphonService.ResetTraffic(req.Code, req.All))
}

// psiphonTest answers once every enabled country was measured, within PsiphonTestLimit.
func (a *RelayController) psiphonTest(c *gin.Context) {
	ctx, cancel := context.WithTimeout(context.Background(), psiphonWriteLimit(c, service.PsiphonTestLimit))
	defer cancel()
	results, err := a.psiphonService.TestEnabled(ctx)
	jsonMsgObj(c, I18nWeb(c, "pages.relays.psiphon.toasts.test"), results, err)
}

// psiphonScan answers at once; the card follows the scan in the state it polls.
func (a *RelayController) psiphonScan(c *gin.Context) {
	jsonMsg(c, I18nWeb(c, "pages.relays.psiphon.toasts.scan"), a.psiphonService.StartScan())
}
