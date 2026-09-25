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
TIMEOUT, NETWORK_ERROR, HTML_TOO_LARGE, REDIRECT_ERROR, PAGE_LIMIT,
OTHER_HTTP_STATUS
```

```powershell
go run ./cmd/graphsite --slow-delay "5s"
```

В другом терминале:

```powershell
go run ./cmd/crawler --url "http://127.0.0.1:8080/index.html" --depth 3 --max-pages 100 --max-redirects 10 --concurrency 4 --max-queue 1000 --timeout "2s" --max-html-bytes 2097152
```

Стандартное автоматическое следование redirect не используется. Crawler сам читает `Location`, разрешает относительный адрес через `net/url`, записывает каждый ответ `3xx`, обнаруживает цикл и соблюдает `--max-redirects`. Конечный HTML разбирается относительно конечного URL. Внешняя redirect-цель сохраняется, но не запрашивается.

Общий fetch-кэш гарантирует, что один нормализованный внутренний URL запрашивается не более одного раза, даже если он встречается и как обычная ссылка, и внутри redirect-цепочки. `--max-pages` расходуют HTML-страницы, redirect и ошибки. Успешный non-HTML-ресурс лимит не расходует: тип определяется по HTTP-заголовку `Content-Type`, а не по расширению URL. Это одинаково работает для JPG, PNG, MP4, CSS, JavaScript, `application/octet-stream` и адреса без расширения вроде `/asset`.

Чтобы узнать `Content-Type`, crawler должен начать HTTP-запрос и получить заголовки. Поэтому фактических диагностических HTTP-запросов может быть больше, чем `Pages checked`. `Pages checked` — это счётчик результатов, принятых в бюджет страниц, а не счётчик всех сетевых операций. Тело успешного non-HTML-ответа, включая `application/pdf`, не считывается и HTML-парсеру не передаётся. URL с окончанием `.pdf`, который возвращает `text/html`, обрабатывается как обычная HTML-страница.

Scheduler единолично управляет ограниченным LIFO-стеком, глубинами, уже запущенными URL и результатами. `--max-queue` ограничивает число готовых заданий планировщика; большинство передаются worker, но повторное расширение уже проверенной страницы может выполняться самим scheduler. `--concurrency` ограничивает число активных HTTP-заданий. Канал `jobs` небуферизован, поэтому одновременно ожидают не больше `max-queue` готовых заданий и выполняются не больше `concurrency` HTTP-заданий. Если готовая очередь заполнена, scheduler сохраняет позицию в списке ссылок страницы и продолжает добавлять оставшиеся ссылки по мере освобождения мест. Если URL уже ожидает в другой ветви, задание поднимается по приоритету без дублирования. Само переполнение не теряет URL; ограничения `--depth` и `--max-pages` действуют как прежде. При `--concurrency 1` сохраняется строгий последовательный DFS, а при большем значении используется DFS-приоритет с параллельной обработкой нескольких ветвей.

В сводке печатаются `Maximum queue`, `Peak queue` и `Queue limit reached`. Последнее поле показывает, что во время обхода готовая очередь достигала лимита и scheduler откладывал добавление ссылок. Это не означает пропуск ссылок.

Потокобезопасный fetch-кэш использует отдельную singleflight-запись на один HTTP URL. Если direct-ссылка и redirect-цепочка одновременно приходят к одному target, реальный запрос выполняется один раз, а остальные workers получают тот же результат.

`Ctrl+C` отменяет scheduler, workers и активные HTTP-запросы через общий `context.Context`. CLI завершается с кодом `130` и не печатает частичный отчёт. Успешный отчёт содержит `Elapsed` — время от входа в `Crawl` до завершения всех workers и сборки результата.

HTML читается через `io.LimitReader`: по умолчанию не более 2 МиБ на ответ. Если сервер прислал больше, страница сохраняет HTTP-статус, получает отдельный результат `HTML_TOO_LARGE`, не считается broken, а усечённый документ не разбирается и его ссылки не попадают в очередь. Лимит меняется параметром `--max-html-bytes`.

Сводка сохраняет общие счётчики `HTTP 4xx` и `HTTP 5xx`, а ниже показывает детальные счётчики для реально встретившихся кодов, например `HTTP 404`, `HTTP 413`, `HTTP 500`.

Документация: [этап 5](docs/crawler/stage-05.md), [этап 6](docs/crawler/stage-06.md), [этап 7](docs/crawler/stage-07.md), [наборы демонстрационных проверок](docs/crawler/demo-checks.md), [памятка для защиты](docs/crawler/defense.md).

## Правило внутренней ссылки

Ссылка считается внутренней, если совпадает `hostname`. Схема и порт в это правило не входят:

```text
http://example.com:8080 → https://example.com:8443 — INTERNAL
https://example.com → http://example.com:8080 — INTERNAL
https://other.example.com → EXTERNAL
```

Поэтому crawler продолжает обход при смене `http`/`https` и порта, если имя хоста осталось тем же. Внешним считается только HTTP(S)-адрес с другим hostname.

## Сценарии тестового сайта

- 10 обычных HTML-страниц с `<h1>`;
- цикл `/a.html → /b.html → /c.html → /a.html`;
- несколько страниц, ведущих на `/hub.html`;
- глубокая цепочка до `/deep-target.html`;
- `404`, `500`, одиночный redirect и цепочка из двух redirects;
- медленная страница и внешняя ссылка;

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
│   │   ├── queue_test.go
│   │   ├── redirect_test.go
│   │   └── results_test.go
│   └── crawlercli/
│       ├── queue_test.go
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
