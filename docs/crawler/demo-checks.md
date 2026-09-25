# Наборы проверок для демонстрации crawler

Все команды выполняются из корня проекта `graph-test-site-go` в PowerShell.

## Подготовка

В первом терминале запустите тестовый сайт:

```powershell
go run ./cmd/graphsite -addr "127.0.0.1:8080" -slow-delay "1s"
```

Ожидаемое сообщение:

```text
Graph test site: http://127.0.0.1:8080/index.html
Slow page delay: 1s
```

Все следующие команды, кроме отдельной проверки `Ctrl+C`, выполняются во втором терминале при работающем сайте.

## Набор 1. Проверка маршрутов тестового сайта

Задайте базовый адрес:

```powershell
$base = "http://127.0.0.1:8080"
```

### Обычная страница, 404 и 500

```powershell
curl.exe -s -o NUL -w "index=%{http_code}`n" "$base/index.html"
curl.exe -s -o NUL -w "missing=%{http_code}`n" "$base/missing-page.html"
curl.exe -s -o NUL -w "server-error=%{http_code}`n" "$base/server-error"
```

Ожидается:

```text
index=200
missing=404
server-error=500
```

### Одиночный redirect

```powershell
curl.exe -s -o NUL -w "status=%{http_code}; target=%{redirect_url}`n" "$base/redirect-once"
```

Ожидается статус `302` и цель `/hub.html`.

### Цепочка из двух redirect

```powershell
curl.exe -s -L -o NUL -w "status=%{http_code}; redirects=%{num_redirects}; final=%{url_effective}`n" "$base/redirect-chain/start"
```

Ожидается:

```text
status=200; redirects=2; final=http://127.0.0.1:8080/c.html
```

## Набор 2. Полный успешный обход графа

```powershell
go run ./cmd/crawler `
  --url "http://127.0.0.1:8080/index.html" `
  --depth 3 `
  --max-pages 100 `
  --max-redirects 10 `
  --concurrency 4 `
  --timeout "300ms" `
  --max-html-bytes 2097152
```

При `slow-delay=1s` и `timeout=300ms` основные значения первого уровня должны быть следующими:

```text
Maximum depth:      3
Max pages reached:  false
Pages checked:      14
Links discovered:   22
Unique HTTP links:  15
Internal links:     21
External links:     1
Successful:         10
Redirect chains:    2
Redirect hops:      3
Broken links:       3
HTTP 4xx:           1
HTTP 404:           1
HTTP 5xx:           1
HTTP 500:           1
Timeouts:           1
```

Во втором уровне отчёта необходимо показать:

- `/missing-page.html` с результатом `HTTP_4XX` и статусом `404 Not Found`;
- `/server-error` с результатом `HTTP_5XX` и статусом `500 Internal Server Error`;
- `/slow.html` с результатом `TIMEOUT`;
- `/redirect-once`: один переход `302` к `/hub.html`, затем `200 OK`;
- `/redirect-chain/start`: два перехода `302`, затем `200 OK` на `/c.html`;
- строку `[EXTERNAL]` для `https://example.com/`;
- ссылки на `/hub.html` со страниц `/index.html`, `/a.html` и `/b.html`;

Значение `Pages checked` равно `14`, причём промежуточный URL `/redirect-chain/middle` тоже является отдельным HTTP-запросом. В отчёте redirect-цепочка при этом представлена как один логический результат исходной ссылки.

## Набор 3. Timeout и успешный медленный ответ

Короткий timeout:

```powershell
go run ./cmd/crawler --url "$base/index.html" --depth 3 --max-pages 100 --max-redirects 10 --concurrency 4 --timeout "300ms"
```

Ожидается `Timeouts: 1`, `Broken links: 3`, `Successful: 10`.

Timeout больше задержки сервера:

```powershell
go run ./cmd/crawler --url "$base/index.html" --depth 3 --max-pages 100 --max-redirects 10 --concurrency 4 --timeout "2s"
```

Ожидается `Timeouts: 0`, `Broken links: 2`, `Successful: 11`. После успешной загрузки `/slow.html` crawler дополнительно извлечёт её ссылку, поэтому `Links discovered` увеличится с `22` до `23`.

## Набор 4. Ограничение глубины

Сначала выполните обход с глубиной `3`:

```powershell
go run ./cmd/crawler --url "$base/index.html" --depth 3 --max-pages 100 --max-redirects 10 --concurrency 4 --timeout "300ms"
```

Ссылка `/deep-target.html` будет обнаружена на глубине `4`, но отдельного блока `PAGE` для неё не будет. Ожидается `Pages checked: 14`.

Затем разрешите глубину `4`:

```powershell
go run ./cmd/crawler --url "$base/index.html" --depth 4 --max-pages 100 --max-redirects 10 --concurrency 4 --timeout "300ms"
```

Теперь `/deep-target.html` должна появиться как проверенная страница с `Depth: 4` и `Result: SUCCESS`. Ожидается:

```text
Pages checked:      15
Links discovered:   23
Successful:         11
```

Граница включительная: при `--depth 3` обрабатываются глубины `0`, `1`, `2`, `3`; глубина `4` только обнаруживается.

## Набор 5. Ограничение количества страниц

Для воспроизводимого порядка используется один worker:

```powershell
go run ./cmd/crawler --url "$base/index.html" --depth 4 --max-pages 5 --max-redirects 10 --concurrency 1 --timeout "300ms"
```

Ожидается:

```text
Maximum pages:      5
Max pages reached:  true
Pages checked:      5
```

Crawler должен корректно завершиться, а `Pages checked` не должен превышать `5`. Для определения `Content-Type` crawler может получить заголовки дополнительных URL; их HTML-body не читается и не разбирается. Успешные non-HTML-ответы в бюджет не входят.

Ограничение очереди проверяется отдельно, например:

```powershell
go test -count=1 -v -run 'Queue' ./tests/crawler ./tests/crawlercli
```

Тесты подтверждают `Peak queue <= Maximum queue`, отсутствие потери URL при заполнении очереди, сохранение DFS при одном worker и то, что одинаковый URL занимает один слот. `Queue limit reached: true` означает задержку постановки заданий, а не неполный обход.

## Набор 6. DFS, цикл и повторяющиеся URL

Строгий DFS демонстрируется с одним worker:

```powershell
go run ./cmd/crawler --url "$base/index.html" --depth 3 --max-pages 100 --max-redirects 10 --concurrency 1 --timeout "300ms"
```

Начало порядка страниц должно быть таким:

```text
/index.html
/a.html
/b.html
/c.html
```

Переход `/c.html -> /a.html` замыкает цикл, но страницы A, B и C проверяются только по одному разу. `/hub.html` также запрашивается один раз, хотя на него ссылаются `/index.html`, `/a.html` и `/b.html`.

При `--concurrency` больше единицы сохраняется DFS-приоритет выдачи заданий, но порядок завершения параллельных ветвей может отличаться.

## Набор 7. Ограничение redirect

```powershell
go run ./cmd/crawler --url "$base/index.html" --depth 3 --max-pages 100 --max-redirects 1 --concurrency 1 --timeout "300ms"
```

Одиночный redirect завершится успешно, а цепочка из двух redirect будет остановлена. Ожидается:

```text
Redirect errors:    1
Broken links:       4
```

В деталях будет сообщение:

```text
maximum redirects exceeded: limit is 1
```

## Набор 8. Ограничение конкурентности

Эта проверка использует управляемый тестовый сервер и измеряет фактический пик активных запросов:

```powershell
go test -count=1 -v -run '^TestConcurrentCrawlHonorsLimitAndActuallyOverlaps$' ./tests/crawler
```

Тест запускает crawler с `concurrency=3`, подтверждает одновременное выполнение трёх запросов и проверяет, что четвёртый запрос не начинается до освобождения worker.

Строгий DFS при одном worker проверяется отдельно:

```powershell
go test -count=1 -v -run '^TestConcurrencyOnePreservesStrictDFS$' ./tests/crawler
```

## Набор 9. Отмена через Ctrl+C

Остановите тестовый сайт и снова запустите его с большой задержкой:

```powershell
go run ./cmd/graphsite -addr "127.0.0.1:8080" -slow-delay "30s"
```

Во втором терминале запустите:

```powershell
go run ./cmd/crawler `
  --url "http://127.0.0.1:8080/index.html" `
  --depth 4 `
  --max-pages 100 `
  --max-redirects 10 `
  --concurrency 4 `
  --timeout "60s"
```

Во время ожидания нажмите `Ctrl+C`. Ожидается немедленное завершение с сообщением:

```text
crawl cancelled
```

Частичный отчёт печататься не должен. Код возврата CLI и остановка активных запросов автоматически проверяются командами:

```powershell
go test -count=1 -v -run '^TestRunReturns130WhenContextIsCancelled$' ./tests/crawlercli
go test -count=1 -v -run '^TestCancellationStopsConcurrentRequestsAndQueuedJobs$' ./tests/crawler
```

## Набор 10. Специализированные автоматические проверки

Полный тест реального граф-сайта:

```powershell
go test -count=1 -v -run '^TestFinalAcceptanceAgainstGraphSite$' ./tests/crawler
```

Redirect-цепочка со всеми промежуточными статусами:

```powershell
go test -count=1 -v -run '^TestRedirectChainKeepsEveryHopAndFinalResult$' ./tests/crawler
```

Нормализация относительных URL и правило scope по hostname:

```powershell
go test -count=1 -v -run '^(TestNormalizeURLRulesThroughPublicAPI|TestScopeUsesHostnameRegardlessOfSchemeOrPort)$' ./tests/crawler
```

Продолжение обхода после redirect с HTTP на HTTPS:

```powershell
go test -count=1 -v -run '^TestHTTPRedirectUpgradeToHTTPSStaysInScopeAndContinuesCrawl$' ./tests/crawler
```

Детализация `404`, `413`, `500` и `503` в сводке:

```powershell
go test -count=1 -v -run '^TestRunPrintsSpecificHTTPStatusCountsInOrder$' ./tests/crawlercli
```

Ограничение HTML как для ответа с точным размером, так и для chunked body без `Content-Length`:

```powershell
go test -count=1 -v -run '^TestHTMLBodyLimitAcceptsExactSizeAndRejectsLargerChunkedBody$' ./tests/crawler
go test -count=1 -v -run '^TestRunReportsHTMLBodyLimit$' ./tests/crawlercli
```

Проверка PDF по HTTP `Content-Type`, включая URL с `.pdf` и без расширения, а также разрешение ссылок через `<base href>`:

```powershell
go test -count=1 -v -run '^(TestHTMLAtPDFSuffixIsParsedAndConsumesPageBudget|TestPDFSuffixStartURLUsesResponseContentType|TestPDFSuffixHTTPErrorIsReported|TestRedirectToPDFSuffixWithHTMLIsFollowedAndParsed|TestRedirectToPDFContentTypeIsRecordedWithoutReadingBody|TestInspectStartResolvesLinksAgainstFirstBaseHref)$' ./tests/crawler
```

Успешные non-HTML MIME-типы не расходуют `max-pages`, а `Content-Type` важнее расширения URL:

```powershell
go test -count=1 -v -run '^(TestCrawlChecksBinaryContentWithoutParsingIt|TestSuccessfulNonHTMLResourcesDoNotConsumeMaxPages|TestContentTypeDeterminesHTMLRegardlessOfFileExtension|TestConcurrentNonHTMLResponsesDoNotSpendPageBudget)$' ./tests/crawler
```

Ограниченная очередь scheduler без потери ссылок и поля отчёта:

```powershell
go test -count=1 -v -run 'Queue' ./tests/crawler ./tests/crawlercli
```

Сетевая ошибка без остановки остального обхода:

```powershell
go test -count=1 -v -run '^TestCrawlClassifiesNetworkErrorAndContinues$' ./tests/crawler
```

Общий URL из direct-ссылки и redirect запрашивается один раз:

```powershell
go test -count=1 -v -run '^TestConcurrentDirectAndRedirectTargetFetchedOnce$' ./tests/crawler
```

## Набор 11. Финальная проверка перед защитой

```powershell
go test -count=1 ./...
go vet ./...
go test -race ./...
```

Ожидается успешное выполнение всех команд без предупреждений race detector. На Windows для `go test -race` требуется GCC, например из MinGW-w64, потому что race detector использует CGO.

## Короткий порядок демонстрации на защите

Если время ограничено, достаточно показать:

1. статусы `200`, `404`, `500` и redirect через `curl.exe`;
2. полный обход из набора 2 и оба уровня отчёта;
3. разницу между `--depth 3` и `--depth 4`;
4. остановку на `--max-pages 5`;
5. тест фактической конкурентности;
6. ручную остановку через `Ctrl+C`;
7. `go test -count=1 ./...` и `go test -race ./...`.
