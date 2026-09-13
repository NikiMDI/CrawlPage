package site

import (
	"fmt"
	"net/http"
	"strings"
	"time"
)

type Config struct {
	SlowDelay time.Duration
}

type handler struct {
	pages     map[string]page
	renderer  renderer
	assets    http.Handler
	slowDelay time.Duration
}

func NewHandler(config Config) (http.Handler, error) {
	if config.SlowDelay <= 0 {
		return nil, fmt.Errorf("slow delay must be positive")
	}

	pageRenderer, err := newRenderer()
	if err != nil {
		return nil, fmt.Errorf("create HTML renderer: %w", err)
	}

	assetHandler, err := newAssetHandler()
	if err != nil {
		return nil, fmt.Errorf("create asset handler: %w", err)
	}

	return &handler{
		pages:     createPages(),
		renderer:  pageRenderer,
		assets:    assetHandler,
		slowDelay: config.SlowDelay,
	}, nil
}

func (h *handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		h.renderer.writePage(w, r, http.StatusMethodNotAllowed, page{
			title: "405 — Метод не поддерживается",
			text:  "Этот тестовый сервер принимает только GET и HEAD-запросы.",
			links: []link{{label: "Вернуться на старт", href: "/index.html"}},
		})
		return
	}

	if strings.HasPrefix(r.URL.Path, "/assets/") {
		h.assets.ServeHTTP(w, r)
		return
	}

	switch r.URL.Path {
	case "/":
		writeRedirect(w, http.StatusFound, "/index.html")
		return
	case "/redirect-once":
		writeRedirect(w, http.StatusFound, "/hub.html")
		return
	case "/redirect-chain/start":
		writeRedirect(w, http.StatusFound, "/redirect-chain/middle")
		return
	case "/redirect-chain/middle":
		writeRedirect(w, http.StatusFound, "/c.html")
		return
	case "/server-error":
		h.renderer.writePage(w, r, http.StatusInternalServerError, page{
			title: "500 — Внутренняя ошибка сервера",
			text:  "Этот ответ намеренно возвращает статус 500 для проверки обработчика ошибок обходчика.",
			links: []link{{label: "Вернуться на старт", href: "/index.html"}},
		})
		return
	}

	currentPage, found := h.pages[r.URL.Path]
	if !found {
		h.renderer.writePage(w, r, http.StatusNotFound, page{
			title: "404 — Страница не найдена",
			text:  "Такого маршрута нет. Эта страница нужна для проверки битых ссылок.",
			links: []link{{label: "Вернуться на старт", href: "/index.html"}},
		})
		return
	}

	if r.URL.Path == "/slow.html" {
		timer := time.NewTimer(h.slowDelay)
		defer timer.Stop()

		select {
		case <-timer.C:
		case <-r.Context().Done():
			return
		}
	}

	h.renderer.writePage(w, r, http.StatusOK, currentPage)
}

func writeRedirect(w http.ResponseWriter, status int, location string) {
	w.Header().Set("Location", location)
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
}
