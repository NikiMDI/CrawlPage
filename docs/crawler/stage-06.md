# Этап 6. Cancellation и корректное завершение

Цель этапа — гарантировать остановку всего конкурентного обхода по отмене родительского `context.Context`, в том числе при Ctrl+C, активных HTTP-запросах и ожидании общего fetch-кэша.

## Путь сигнала отмены

```text
Ctrl+C / SIGTERM
        │
        ▼
signal.NotifyContext
        │
        ▼
crawlercli.Run
        │
        ▼
Inspector.Crawl
        │
        ├── Scheduler
        ├── Worker 1 ... Worker N
        ├── request context
        └── singleflight waiters
```

`cmd/crawler/main.go` создаёт контекст через `signal.NotifyContext`. Тот же контекст передаётся в CLI и далее во все уровни crawler.

При пользовательской отмене CLI:

- не печатает частичный итоговый отчёт;
- выводит `crawl cancelled` в stderr;
- возвращает код завершения `130`, традиционно используемый для процесса, остановленного Ctrl+C.

## Scheduler

Scheduler проверяет `ctx.Err()` перед выдачей работы и ожидает результаты через `select` с `ctx.Done()`.

После отмены он:

1. больше не расширяет очередь;
2. возвращает `context.Canceled`;
3. передаёт управление отложенной очистке в `Crawl`.

При принятии worker-result сначала проверяется `workerResult.Err`. Незавершённая страница не добавляется в `result.Pages`. Это особенно важно, когда первый redirect-hop уже получен, а чтение тела конечной страницы отменено.

## Workers

Каждый worker проверяет контекст:

- при ожидании задания;
- сразу после получения задания;
- при отправке результата scheduler.

Поэтому уже отменённое задание не начинает новый fetch, даже если `select` одновременно видел готовый `jobs` и закрытый `ctx.Done()`.

## HTTP и timeout

Каждый запрос получает дочерний контекст:

```go
requestContext, cancel := context.WithTimeout(parent, RequestTimeout)
```

Есть два разных результата:

- истёк только `RequestTimeout` — URL получает `TIMEOUT`, обход продолжается;
- отменён родительский контекст — весь `Crawl` возвращает `context.Canceled`.

Глобальная отмена не записывается как `TIMEOUT`, `NETWORK_ERROR` или broken link.

## Singleflight waiter

Если один worker выполняет `/shared`, другой worker может ждать ту же cache entry:

```go
select {
case <-entry.ready:
    return entry.result, true, entry.err
case <-ctx.Done():
    return fetchedURL{}, true, ctx.Err()
}
```

Ожидающий worker освобождается напрямую через `ctx.Done()`. Worker-владелец отменяет HTTP, публикует ошибку и закрывает `entry.ready`. Mutex во время HTTP или ожидания не удерживается.

## Порядок очистки

После выхода scheduler функция `Crawl` всегда выполняет:

```text
cancelWorkers()
close(jobs)
workers.Wait()
сбор статистики и проблем
```

Канал `workerResults` закрывать не требуется. Его единственные отправители — workers, а `workers.Wait()` гарантирует, что после возврата из `Crawl` отправителей больше нет.

## Тесты этапа

`tests/crawler/cancellation_test.go` проверяет:

1. отмену четырёх одновременно выполняемых запросов;
2. отсутствие запуска URL, оставшегося в очереди;
3. завершение активных HTTP-handlers;
4. освобождение worker, ожидающего общий `/shared`;
5. один физический запрос к общему target;
6. отмену во время чтения тела после redirect;
7. отсутствие частичного `PageResult` и ложных проблем;
8. заполнение `Elapsed` даже при отмене.

CLI-тест проверяет код `130`, сообщение `crawl cancelled` и отсутствие частичного stdout-отчёта. Ранее существовавший тест заранее отменённого контекста запускается с несколькими workers и подтверждает ноль HTTP-запросов.

## Ручная демонстрация

```powershell
go run ./cmd/graphsite --slow-delay "30s"
```

```powershell
go run ./cmd/crawler `
  --url "http://127.0.0.1:8080/index.html" `
  --depth 4 `
  --max-pages 100 `
  --concurrency 4 `
  --timeout "60s"
```

Во время активных запросов нужно нажать Ctrl+C. Процесс должен завершиться сразу с сообщением `crawl cancelled`, не ожидая ни `--slow-delay`, ни `--timeout`.
