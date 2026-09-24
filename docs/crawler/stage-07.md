# Этап 7. Финальный отчёт и приёмочные проверки

Седьмой этап завершает лабораторную работу: добавляет измерение времени, стабилизирует вывод источников, оптимизирует завершение после `max-pages` и проверяет весь тестовый граф одним конкурентным запуском.

## `Elapsed`

В `CrawlResult` добавлено поле:

```go
Elapsed time.Duration
```

Время измеряется от входа в `Inspector.Crawl` до окончания отложенной очистки:

1. scheduler завершился;
2. workers получили cancellation/закрытие `jobs`;
3. `workers.Wait()` дождался всех worker-goroutine;
4. собрана статистика;
5. страницы отсортированы и сформированы проблемы.

Таким образом, `Elapsed` не заканчивается раньше реального завершения crawler. В CLI длительность округляется до микросекунд для очень быстрого запуска и до миллисекунд для обычного обхода.

## Финальный двухуровневый отчёт

Уровень 1 содержит агрегаты:

```text
Start URL
Maximum depth
Maximum pages
Maximum redirects
Concurrency
Elapsed
Pages checked
Links discovered
Unique HTTP links
Internal / External links
Successful
Redirect chains / hops
Broken links
HTTP 4xx / 5xx
Timeouts / Network errors
```

Уровень 2 содержит:

- результат каждого логического URL;
- глубину и фактическую `FetchDepth`, если они различаются;
- нормализованные внутренние и внешние ссылки;
- каждую redirect-цепочку со статусами переходов;
- каждую проблему и все страницы-источники.

Источники перед печатью сортируются. Порядок их обнаружения при конкурентной работе может зависеть от скорости ответов, но текст отчёта остаётся стабильнее.

## Поведение после `max-pages`

Допуск page-like результата остаётся атомарным под mutex и выполняется после получения HTTP-статуса и `Content-Type`. HTML, redirect и ошибки расходуют бюджет; успешный non-HTML — нет.

Поле отчёта `MaxPagesReached` становится `true`, когда crawler действительно отклонил хотя бы один новый URL из-за лимита. Если проверено ровно `max-pages` URL, но следующего кандидата не было, значение остаётся `false`: лимит был достигнут численно, но не остановил обход.

Чтобы отличить HTML от extensionless ресурса, scheduler продолжает выдавать обнаруженные URL: HTTP-заголовки могут быть получены, но тело HTML сверх бюджета не читается и не разбирается. Поэтому физических запросов может быть больше `PagesChecked`. URL с существующей fetch-entry по-прежнему обрабатывается без повторного HTTP-запроса.

Ожидающий `frontier` отдельно ограничен `MaxQueue`; канал `jobs` небуферизован, а активная работа ограничена `Concurrency`. При переполнении очередь не растёт: ссылка учитывается в счётчике `Skipped by queue` и может быть принята при более позднем повторном обнаружении. Crawler по-прежнему хранит обнаруженные рёбра для графа и отчёта, поэтому общий объём отчёта зависит от количества href, уже прочитанных из HTML.

## Финальный приёмочный тест

`tests/crawler/acceptance_test.go` запускает реальный `internal/site` через `httptest.Server` с:

```text
depth       = 3
max-pages   = 100
concurrency = 4
timeout     < slow-delay
```

Один тест подтверждает:

- обычные `200 OK` страницы;
- `404 Not Found`;
- `500 Internal Server Error`;
- `TIMEOUT` медленной страницы;
- одиночный redirect;
- цепочку из двух redirects;
- отсутствие бесконечного обхода A → B → C → A;
- одну внешнюю ссылку без внешнего HTTP-запроса;
- несколько источников `/hub.html`;
- обнаружение `/deep-target.html` на глубине 4 без запроса при лимите 3;
- ненулевой `Elapsed`;
- соблюдение лимитов и отсутствие повторных HTTP-запросов.

Строгий порядок страниц намеренно не проверяется при `concurrency=4`: scheduler сохраняет DFS-приоритет, но порядок завершения параллельных ветвей зависит от HTTP.

## Матрица требований

| Требование | Реализация | Основные тесты |
|---|---|---|
| Внутренний граф | `scheduler.go`, `DepthByURL` | `crawl_test.go`, `graphsite_test.go` |
| Нормализация URL | `normalize.go` | `normalize_test.go` |
| Same-scope и HTTP→HTTPS upgrade | `scope.go` | `normalize_test.go`, `redirect_test.go` |
| Максимальная глубина | `scheduler.expand` | `crawl_test.go`, `acceptance_test.go` |
| `max-pages` | `crawlSession.fetch` | `crawl_test.go`, `concurrency_test.go` |
| Конкурентность | fixed worker pool | `concurrency_test.go` |
| Повторные URL и циклы | scheduler + fetch cache | `crawl_test.go`, `concurrency_test.go` |
| 4xx/5xx с детализацией, network/timeout | `classify.go`, `fetch.go`, CLI | `results_test.go`, CLI tests |
| Ограничение HTML body | `fetch.go`, `MaxHTMLBytes` | `results_test.go`, CLI tests |
| Redirect-цепочки | `inspectURL` | `redirect_test.go`, `acceptance_test.go` |
| Внешние ссылки | `scope.contains` | `crawl_test.go`, `acceptance_test.go` |
| Источники проблем | `recordSources` | `results_test.go`, `acceptance_test.go` |
| Ctrl+C/cancellation | context propagation | `cancellation_test.go`, CLI tests |
| Корректное завершение | cancel + close + WaitGroup | `cancellation_test.go` |
| Двухуровневый отчёт | `crawlercli.writeReport` | `tests/crawlercli/run_test.go` |
| `Elapsed` | `CrawlResult.Elapsed` | acceptance и CLI tests |

## Команды проверки

```powershell
go test ./...
go vet ./...
go test -count=20 ./tests/crawler -run "Concurrent|Cancellation|Acceptance"
go test "-coverpkg=./internal/crawler,./internal/crawlercli" ./tests/...
go test -race ./...
```

На Windows race detector требует `CGO_ENABLED=1` и C-компилятор MinGW-w64/GCC. Если компилятора нет, обычные тесты и `vet` выполняются, но успешный race-прогон заявлять нельзя.
