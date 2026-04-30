package command

import (
	"context"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/Banezzz/ezpay/bootstrap"
	"github.com/Banezzz/ezpay/config"
	"github.com/Banezzz/ezpay/install"
	"github.com/Banezzz/ezpay/middleware"
	"github.com/Banezzz/ezpay/route"
	"github.com/Banezzz/ezpay/util/constant"
	luluHttp "github.com/Banezzz/ezpay/util/http"
	"github.com/Banezzz/ezpay/util/log"
	"github.com/labstack/echo/v4"
	echoMiddleware "github.com/labstack/echo/v4/middleware"
	"github.com/spf13/cobra"
)

var httpCmd = &cobra.Command{
	Use:   "http",
	Short: "http service",
	Long:  "http service commands",
	Run: func(cmd *cobra.Command, args []string) {
	},
}

func init() {
	httpCmd.AddCommand(startCmd)
}

var startCmd = &cobra.Command{
	Use:   "start",
	Short: "start",
	Long:  "start http service",
	Run: func(cmd *cobra.Command, args []string) {
		// If no config file exists, or if install=true is set in the config,
		// run the first-run install API on the same port as the main server.
		// The wizard writes the .env (with install=false) and shuts itself
		// down so bootstrap.InitApp() can read it normally on the same port.
		if config.NeedsInstall() {
			envPath, _ := config.ResolveConfigPath()
			install.RunInstallServer(install.DefaultInstallAddr, envPath)
		}
		bootstrap.InitApp()
		printBanner()
		HttpServerStart()
	},
}

func HttpServerStart() {
	paymentServer := newEchoServer()
	MiddlewareRegister(paymentServer)
	route.RegisterPublicRoutes(paymentServer)
	paymentServer.Static(config.StaticPath, config.StaticFilePath)
	registerPaymentSPA(paymentServer)

	adminServer := newEchoServer()
	MiddlewareRegister(adminServer)
	route.RegisterInternalRoutes(adminServer)
	route.RegisterAdminRoutes(adminServer)
	registerAdminSPA(adminServer)

	startEchoServer("payment", paymentServer, config.GetHTTPListen())
	startEchoServer("admin", adminServer, config.GetAdminHTTPListen())

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, os.Interrupt, syscall.SIGTERM)
	<-quit

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	for _, server := range []*echo.Echo{paymentServer, adminServer} {
		if err := server.Shutdown(ctx); err != nil {
			server.Logger.Fatal(err)
		}
	}
}

func newEchoServer() *echo.Echo {
	e := echo.New()
	e.HideBanner = true
	e.HTTPErrorHandler = customHTTPErrorHandler
	return e
}

func registerPaymentSPA(e *echo.Echo) {
	registerSPA(e, func(path string) bool {
		if path == "/cashier" || strings.HasPrefix(path, "/cashier/") {
			return false
		}
		if strings.HasPrefix(path, "/assets/") || strings.HasPrefix(path, "/images/") || isWWWRootAsset(path) {
			return false
		}
		return true
	})
}

func registerAdminSPA(e *echo.Echo) {
	registerSPA(e, func(path string) bool {
		if path == "/install" || strings.HasPrefix(path, "/install/") {
			// The install wizard is only served by install.RunInstallServer
			// before bootstrap. Once main server starts, block /install.
			return true
		}
		return luluHttp.ShouldSkipSPAFallback(path)
	})
}

func registerSPA(e *echo.Echo, shouldSkip func(path string) bool) {
	// Resolve www/ relative to the executable so SPA routes work regardless
	// of the working directory. main.go extracts www/ next to the binary.
	wwwRoot := "./www"
	if exePath, err := os.Executable(); err == nil {
		if exePath, err = filepath.EvalSymlinks(exePath); err == nil {
			wwwRoot = filepath.Join(filepath.Dir(exePath), "www")
		}
	}
	e.Use(echoMiddleware.StaticWithConfig(echoMiddleware.StaticConfig{
		Skipper: func(c echo.Context) bool {
			return shouldSkip(c.Request().URL.Path)
		},
		HTML5: true,
		Index: "index.html",
		Root:  wwwRoot,
	}))
}

func isWWWRootAsset(path string) bool {
	switch path {
	case "/apple-touch-icon.png",
		"/favicon-16x16.png",
		"/favicon-32x32.png",
		"/favicon.ico",
		"/manifest.webmanifest",
		"/registerSW.js",
		"/robots.txt",
		"/sw.js":
		return true
	default:
		return strings.HasPrefix(path, "/pwa-") || strings.HasPrefix(path, "/workbox-")
	}
}

func startEchoServer(name string, e *echo.Echo, httpListen string) {
	go func() {
		log.Sugar.Infof("[http] %s server listening on %s", name, httpListen)
		if err := e.Start(httpListen); err != nil && err != http.ErrServerClosed {
			log.Sugar.Errorf("[http] %s server error: %v", name, err)
		}
	}()
}

func MiddlewareRegister(e *echo.Echo) {
	if config.HTTPAccessLog {
		e.Use(echoMiddleware.Logger())
	}
	e.Use(middleware.RequestUUID())
}

func customHTTPErrorHandler(err error, e echo.Context) {
	code := http.StatusInternalServerError
	msg := "server error"
	resp := &luluHttp.Response{
		StatusCode: code,
		Message:    msg,
		RequestID:  e.Request().Header.Get(echo.HeaderXRequestID),
	}
	// echo.HTTPError carries a real HTTP status (401 for auth failures,
	// 404 for missing routes, etc.). Propagate it instead of flattening
	// everything to 200 — clients rely on the status code.
	if he, ok := err.(*echo.HTTPError); ok {
		resp.StatusCode = he.Code
		if s, ok := he.Message.(string); ok {
			resp.Message = s
		} else if he.Message != nil {
			resp.Message = http.StatusText(he.Code)
		}
		_ = e.JSON(he.Code, resp)
		return
	}
	// Internal RspError: propagate Code as both the JSON status_code and
	// the real HTTP status when it maps to one (400/401/...); business
	// codes (>=1000) map to HTTP 400 so clients get a proper 4xx while
	// still reading the granular code from the body.
	if he, ok := err.(*constant.RspError); ok {
		resp.StatusCode = he.Code
		resp.Message = he.Msg
		_ = e.JSON(he.HttpStatus(), resp)
		return
	}
	_ = e.JSON(http.StatusInternalServerError, resp)
}
