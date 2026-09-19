# Тестовый граф-сайт и crawler на Go

Проект содержит завершённую поэтапную реализацию проверяющего ссылки web crawler. В нём находятся два отдельных приложения:

- `cmd/graphsite` — локальный тестовый сайт-граф;
- `cmd/crawler` — конкурентный парсер и проверяющий ссылок.

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

## Crawler: завершённые этапы 6–7

Финальная версия выполняет ограниченный конкурентный обход графа, классифицирует результат каждого внутреннего URL, вручную сохраняет redirect-цепочки, поддерживает cancellation и измеряет полное время работы:

```text
SUCCESS, REDIRECT, HTTP_4XX, HTTP_5XX,
TIMEOUT, NETWORK_ERROR, REDIRECT_ERROR, PAGE_LIMIT,
OTHER_HTTP_STATUS
```

```powershell
go run ./cmd/graphsite --slow-delay "5s"
```

В другом терминале:

```powershell
go run ./cmd/crawler --url "http://127.0.0.1:8080/index.html" --depth 3 --max-pages 100 --max-redirects 10 --concurrency 4 --timeout "2s"
```

Стандартное автоматическое следование redirect не используется. Crawler сам читает `Location`, разрешает относительный адрес через `net/url`, записывает каждый ответ `3xx`, обнаруживает цикл и соблюдает `--max-redirects`. Конечный HTML разбирается относительно конечного URL. Внешняя redirect-цель сохраняется, но не запрашивается.

Общий fetch-кэш гарантирует, что один нормализованный внутренний URL запрашивается не более одного раза, даже если он встречается и как обычная ссылка, и внутри redirect-цепочки. Каждый новый фактически запрошенный URL входит в `--max-pages`; внешний target лимит не расходует.

Scheduler единолично управляет LIFO-стеком, глубинами, уже запущенными URL и результатами. Ровно `--concurrency` workers получают задания через канал; поэтому одновременно выполняется не больше указанного числа HTTP-запросов. При `--concurrency 1` сохраняется строгий последовательный DFS, а при большем значении используется DFS-приоритет с параллельной обработкой нескольких ветвей.

Потокобезопасный fetch-кэш использует отдельную singleflight-запись на один HTTP URL. Если direct-ссылка и redirect-цепочка одновременно приходят к одному target, реальный запрос выполняется один раз, а остальные workers получают тот же результат.

`Ctrl+C` отменяет scheduler, workers и активные HTTP-запросы через общий `context.Context`. CLI завершается с кодом `130` и не печатает частичный отчёт. Успешный отчёт содержит `Elapsed` — время от входа в `Crawl` до завершения всех workers и сборки результата.

Документация: [этап 5](docs/crawler/stage-05.md), [этап 6](docs/crawler/stage-06.md), [этап 7](docs/crawler/stage-07.md), [наборы демонстрационных проверок](docs/crawler/demo-checks.md), [памятка для защиты](docs/crawler/defense.md).

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

## Итоговая структура проекта

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
│   │   ├── fetch.go
│   │   ├── inspect.go
│   │   ├── links.go
│   │   ├── normalize.go
│   │   ├── scheduler.go
│   │   ├── scope.go
│   │   └── worker.go
│   └── crawlercli/
│       └── app.go
├── tests/
│   ├── crawler/
│   │   ├── acceptance_test.go
│   │   ├── cancellation_test.go
│   │   ├── concurrency_test.go
│   │   ├── crawl_test.go
│   │   ├── graphsite_test.go
│   │   ├── links_test.go
│   │   ├── normalize_test.go
│   │   ├── redirect_test.go
│   │   └── results_test.go
│   └── crawlercli/
│       └── run_test.go
├── docs/crawler/
│   ├── stage-02.md
│   ├── stage-05.md
│   ├── stage-06.md
│   ├── stage-07.md
│   ├── demo-checks.md
│   └── defense.md
├── go.mod
├── go.sum
└── README.md
```
