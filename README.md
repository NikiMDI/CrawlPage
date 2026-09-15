# Тестовый граф-сайт и crawler на Go

Проект предназначен для поэтапной разработки проверяющего ссылки web crawler. В нём находятся два отдельных приложения:

- `cmd/graphsite` — локальный тестовый сайт-граф;
- `cmd/crawler` — парсер ссылок, который развивается небольшими рабочими этапами.

Тестовый сайт реализован стандартной библиотекой Go. Для корректного разбора HTML crawler использует `golang.org/x/net/html`.

## Требования

- Go 1.22 или новее;
- GoLand либо терминал.

## Запуск тестового сайта

Из корня проекта:

```powershell
go run ./cmd/graphsite
```

По умолчанию стартовая страница доступна по адресу <http://127.0.0.1:8080/index.html>.

Другой адрес и задержка медленной страницы:

```powershell
go run ./cmd/graphsite -addr "127.0.0.1:9090" -slow-delay "7s"
```

## Crawler: этап 3

Третий этап выполняет последовательный DFS-обход и классифицирует результат проверки каждого внутреннего URL:

```text
SUCCESS, REDIRECT, HTTP_4XX, HTTP_5XX,
TIMEOUT, NETWORK_ERROR, OTHER_HTTP_STATUS
```

```powershell
go run ./cmd/graphsite --slow-delay "100ms"
```

В другом терминале:

```powershell
go run ./cmd/crawler --url "http://127.0.0.1:8080/index.html" --depth 3 --max-pages 100 --timeout "2s"
```

Используется явный LIFO-стек, `visited`, минимальная обнаруженная глубина и сохранение всех страниц-источников. Одна проблемная ссылка представлена одним `Problem`, содержащим все разные страницы-источники. Workers и ручная обработка redirect появятся на следующих этапах. Подробности находятся в [документации этапа 3](docs/crawler/stage-03.md).

## Правило внутренней ссылки

Ссылка считается внутренней, только если совпадают:

```text
scheme + hostname + effective port
```

Например, для `http://localhost:8080` адреса с HTTPS, другим портом или поддоменом считаются внешними.

## Сценарии тестового сайта

- 10 обычных HTML-страниц с `<h1>`;
- цикл `/a.html → /b.html → /c.html → /a.html`;
- несколько страниц, ведущих на `/hub.html`;
- глубокая цепочка до `/deep-target.html`;
- `404`, `500`, одиночный redirect и цепочка из двух redirects;
- медленная страница и внешняя ссылка;
- изображения на страницах глубины 1 и 2;
- PDF-файлы, на которые ссылаются страницы A и B.

## Проверка

```powershell
go test ./...
go vet ./...
go test -race ./...
go test "-coverpkg=./internal/crawler,./internal/crawlercli" ./tests/...
```

Для `go test -race` на Windows требуется установленный C-компилятор, поскольку race detector использует CGO.

Все тесты находятся в отдельной папке `tests`. Они проверяют crawler через публичный API. Логика CLI вынесена в `internal/crawlercli`, поэтому её можно тестировать без запуска дочернего процесса.

## Структура проекта после этапа 3

```text
graph-test-site-go/
├── cmd/
│   ├── graphsite/
│   │   └── main.go
│   └── crawler/
│       └── main.go
├── internal/
│   ├── site/
│   │   ├── assets/
│   │   │   ├── images/
│   │   │   │   ├── depth-1.png
│   │   │   │   └── depth-2.jpg
│   │   │   └── pdf/
│   │   │       ├── a.pdf
│   │   │       └── b.pdf
│   │   ├── assets.go
│   │   ├── handler.go
│   │   ├── pages.go
│   │   └── render.go
│   ├── crawler/
│   │   ├── classify.go
│   │   ├── config.go
│   │   ├── crawl.go
│   │   ├── inspect.go
│   │   ├── links.go
│   │   ├── normalize.go
│   │   └── scope.go
│   └── crawlercli/
│       └── app.go
├── tests/
│   ├── crawler/
│   │   ├── crawl_test.go
│   │   ├── graphsite_test.go
│   │   ├── links_test.go
│   │   ├── normalize_test.go
│   │   └── results_test.go
│   └── crawlercli/
│       └── run_test.go
├── docs/crawler/
│   ├── stage-02.md
│   └── stage-03.md
├── go.mod
├── go.sum
└── README.md
```
