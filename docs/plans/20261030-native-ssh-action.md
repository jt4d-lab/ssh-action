# Нативный SSH action: свой Go-CLI вместо скачивания drone-ssh

## Overview

Задача — заменить `appleboy/ssh-action`, который в рантайме качает бинарь `drone-ssh` с GitHub Releases
(плюс `checksums.txt`, с проверкой и повторами). В Gitea Actions за VPN это регулярно падает по
таймауту и удорожает каждый джоб.

Вместо этого в репозитории лежит собственный статический бинарь, собранный из нашего Go-кода, и
composite action, который его распаковывает и запускает. В рантайме нет ни одного сетевого запроса:
единственная загрузка — обычный чекаут action-репозитория, который act_runner кэширует по `repo@ref`.

Функциональность — полный паритет с `drone-ssh` по входам и поведению, чтобы миграция сводилась к
замене строки `uses:`, плюс три новых входа, которых в upstream нет: рабочая директория на хосте
(`workdir`), её рекурсивное создание (`create_workdir`) и копирование локальных файлов на хост
(`files`).

## Context (from discovery)

- Репозиторий пуст: `README.md` + initial commit, remote `git@github.com:jt4d-lab/ssh-action.git`,
  основная ветка — `master` и локально, и на remote (`origin/HEAD` → `refs/heads/master`; ветка `main`
  существует и указывает на тот же коммит).
- Изучены upstream-исходники: `appleboy/drone-ssh` (`main.go`, `plugin.go`, `DOCS.md`) и
  `appleboy/ssh-action` (`action.yml`, `entrypoint.sh`).
- `drone-ssh` — Go-CLI на `github.com/appleboy/easyssh-proxy` v1.5.3 (MIT) + `urfave/cli`; вся логика
  подключения (proxy jump, fingerprint, ciphers, PTY, таймауты) живёт в `easyssh-proxy`.
- Проверенное по коду `easyssh-proxy` v1.5.3 (файл `easyssh.go`):
  - `Connect()` выполняет dial сам (включая proxy-jump) и возвращает `(*ssh.Session, *ssh.Client, error)`;
    сессия уже с запрошенным PTY, если `RequestPty` включён;
  - `Stream()` внутри вызывает свой `Connect()`, то есть со своим рукопожатием;
  - `Scp`/`WriteFile` умеют только одиночный файл и требуют `scp` на хосте — для каталогов не годятся.
- Замер: собранный `drone-ssh` для linux/amd64 — 7.0 МБ raw, 2.9 МБ gzip, 2.2 МБ xz.
  Отсюда: две арки в gzip ≈ 5.7 МБ на релиз в истории git.
- Найденный баг upstream: `ssh-action/action.yml` передаёт `INPUT_CIPHER`, а `drone-ssh` читает
  `INPUT_CIPHERS` — вход `cipher` фактически не работает. Принимаем оба имени.
- Ограничения платформы (проверено по докам act_runner): `runs.using` — только гейт валидации,
  `node`/`go` в образе не гарантированы, `secrets` внутри composite недоступны. Поэтому composite +
  `shell: sh` + бинарь из `github.action_path` — самый переносимый вариант.

## Development Approach

- **testing approach**: TDD — сначала тест с ожидаемым поведением, затем реализация.
- Полностью завершать задачу до перехода к следующей; маленькие сфокусированные изменения.
- **CRITICAL: каждая задача обязательно содержит новые/обновлённые тесты** — тесты не опциональны.
  Покрываются и успешные, и ошибочные сценарии, включая новые ветки кода.
- **CRITICAL: все тесты зелёные до начала следующей задачи** (последний пункт каждой задачи).
- **CRITICAL: обновлять этот файл плана при изменении объёма работ.**
- Каждый комментарий в коде — только существенный факт; рассуждения, пересказ кода и обоснование
  решений не пишем (правило из `~/.pi/agent/AGENTS.md`). Докблоки — про контракт для вызывающей
  стороны.
- Обратная совместимость: имена входов и семантика из upstream не меняются, кроме явно
  зафиксированных отклонений.

## Testing Strategy

- **unit tests**: обязательны в каждой задаче (`go test ./...`), без сети и docker.
- **проверка shell-семантики**: сгенерированный текст скрипта прогоняется локально через `sh -c` в
  временном каталоге (обычный путь, путь с пробелом, `~/…`). Это единственный способ поймать ошибки
  кавычек и тильды, невидимые фейковому exec-обработчику.
- **интеграционные тесты**: `internal/sshtest` — SSH-сервер на `x/crypto/ssh` в процессе теста
  (пароль, ключ, exec с кодом возврата, stdin, PTY, барьер синхронизации, TCP-форвардер как jump-хост).
  Работают на любом раннере, включая act_runner без вложенного docker.
- **тесты копирования**: обработчик exec, читающий tar из stdin и разворачивающий его через
  `archive/tar` во временный каталог — проверяем наш стрим без зависимости от системного `tar`.
- **e2e action'а**: `testserver` (тот же код сервера как бинарь, включая форвардер) + `uses: ./` в
  workflow, на двух портах, с проверкой всех ключевых входов, включая `workdir`/`create_workdir`/`files`
  и настоящий `tar` на «хосте».
- **shell-часть action'а**: `tests/entrypoint_test.sh` (POSIX sh) на арку, распаковку и
  `capture_stdout` + `shellcheck` в CI.

## Progress Tracking

- отмечать выполненные пункты `[x]` сразу
- новые задачи добавлять с префиксом ➕, блокеры — ⚠️
- держать план в синхроне с фактической работой

## Solution Overview

Три слоя, каждый со своей ответственностью:

1. `action.yml` + `action/entrypoint.sh` — composite-обвязка: маппинг входов в `INPUT_*`, выбор арки,
   распаковка `dist/linux-$arch.gz` в `RUNNER_TEMP`, `exec` бинаря, опциональный `capture_stdout`.
2. `cmd/ssh-action` — CLI: env → `Config` → выполнение → код выхода. Никакой логики SSH.
3. `internal/` — `config` (парсинг/валидация/redaction), `script` (кавычки путей, сборка скрипта,
   env-экспорты, guard'ы `script_stop`, пролог `workdir`), `sshx` (соединение через `easyssh-proxy`,
   собственная сессия exec, аплоад, общий дедлайн хоста, мультихост), `sshtest` (тестовый SSH-сервер).

Ключевые решения:

- **Одно соединение на хост**. `easyssh.MakeConfig.Connect()` используется только чтобы получить
  `*ssh.Client` (dial, proxy-jump, fingerprint, ciphers, `timeout`); возвращённая сессия сразу
  закрывается, а дальше мы сами открываем сессии: одна под аплоад, одна под скрипт. `Stream()` не
  используется — иначе на каждый хост приходится второе рукопожатие, а PTY, запрошенный `Connect()`,
  попал бы на бинарный tar-стрим. Чтение вывода повторяет семантику `Stream` (построчно, с обрезкой
  `\n`), чтобы логи не изменились.
- Свой код на `easyssh-proxy`, а не копия `drone-ssh`: паритет по подключению наследуется от той же
  библиотеки, а фичи дальше добавляются без оглядки на upstream.
- Бинари коммитятся в `dist/`, а не собираются на раннере: в образе act_runner нет гарантированного
  Go, а сборка на джобе добавляет 10–30 с и точку отказа.
- Копирование — tar-пайп в `stdin` отдельной сессии: `archive/tar` в Go локально,
  `mkdir -p <dst> && tar -C <dst> -xf -` на хосте. Новых зависимостей нет, каталоги/симлинки/права
  сохраняются, системный `tar` есть везде, включая busybox. Заголовки пишутся в `tar.FormatGNU`:
  PAX-заголовки длинных путей busybox понимает не полностью.
- `workdir` реализуется прологом скрипта `mkdir -p <q> && cd <q> || exit 1`; `Stream`-семантика
  (один шелл на скрипт) сохранена, поэтому `cd` действует на все последующие строки.
- Только linux/amd64 и linux/arm64 (осознанное ограничение по запросу).
- Ретраев и keepalive в v1 нет: ограничение задаётся `timeout` и `command_timeout`.

## Technical Details

### Входы

Паритет с `appleboy/ssh-action` плюс `script_stop` из `drone-ssh`:

Соединение: `host`, `port` (22), `protocol` (tcp), `username` (root), `password`, `key`, `key_path`,
`passphrase`, `fingerprint`, `timeout` (30s), `cipher`/`ciphers`, `use_insecure_cipher`, `sync`.

Прокси: `proxy_host`, `proxy_port` (22), `proxy_protocol` (tcp), `proxy_username`, `proxy_password`,
`proxy_key`, `proxy_key_path`, `proxy_passphrase`, `proxy_fingerprint`, `proxy_cipher`,
`proxy_use_insecure_cipher`, `proxy_timeout`.

Выполнение: `script`, `script_path`, `script_stop`, `command_timeout` (10m), `envs`, `envs_format`
(`export {NAME}={VALUE}`), `allenvs`, `request_pty`, `debug`, `capture_stdout`.

Новые (в upstream отсутствуют): `workdir`, `create_workdir`, `files`.

Выход: `stdout` (только при `capture_stdout`).

Выпадают: `version` (бинарь наш, версия = тег) и `curl_insecure` (существовал только для скачивания).

### Валидация (код 2, до любых подключений)

- пустой `host` после очистки;
- нет ни одного из `key`, `key_path`, `password`;
- `protocol`/`proxy_protocol` не из `tcp`/`tcp4`/`tcp6`;
- неразбираемые `timeout`, `command_timeout`, `proxy_timeout`, `port`;
- `files` без `workdir`; `create_workdir` без `workdir`;
- `~user/...` в `workdir` (раскрытие чужого домашнего каталога не поддерживаем);
- пустой `script` и пустой `script_path` **и** пустой `files` — нечего делать;
  пустой скрипт при заданном `files` допустим (режим «только копирование»).

### Семантика, воспроизводимая буквально

- `host` — строка через запятую. Правила разделения `host:port`:
  - `tcp`/`tcp4`: `host:port` разделяется `net.SplitHostPort`; IPv6-литерал обязан быть в скобках
    (`[::1]:2222`), иначе — ошибка конфигурации с внятным текстом;
  - `tcp6`: не разделяется никогда (паритет с upstream), `port` берётся из `port`;
  - пустые элементы и пробелы отбрасываются.
- Порядок сборки скрипта: пролог `workdir` → `script` → `script_path`, склейка через `\n`, пустые
  строки отбрасываются.
- `script_stop` вставляет после каждой строки (кроме оканчивающейся на `\`) guard, сохраняющий `$?` и
  выходящий с этим кодом.
- `envs`: имя апперкейсится, значение читается из окружения, подставляется в `envs_format` с
  shell-эскейпингом; отсутствующая переменная пропускается (с предупреждением в stderr — отклонение).
- `allenvs`: переменные с префиксами `DRONE_`, `PLUGIN_`, `INPUT_`, `GITHUB_`. Паритет с upstream, но
  это отправляет на хост `INPUT_KEY`, `INPUT_PASSWORD`, `INPUT_PASSPHRASE`, `GITHUB_TOKEN` — в README
  обязательное предупреждение.
- `debug`: конфигурация с `[REDACTED]` вместо `key`/`password`/`passphrase` (включая proxy), имена
  env без значений, список `files` и текст скрипта как есть.
- Мультихост: параллельно по умолчанию (все хосты стартуют, ошибки собираются, возвращается первая
  после завершения всех), `sync: true` — последовательно с остановкой на первом сбое. Префикс `host: `
  в логе при числе хостов > 1.
- Потоки: удалённый stdout → stdout, удалённый stderr → stderr; служебные сообщения самого action'а
  (версия, ход копирования, финальный итог) → stderr, поэтому `capture_stdout` содержит только вывод
  удалённой команды (отклонение от upstream, где в `stdout` попадает всё).
- Коды выхода: `0` успех; `1` ошибка соединения/копирования/команды/таймаут; `2` невалидная
  конфигурация. В тексте ошибки — хост, путь и удалённый exit status (`tar` в том числе).
- Финал лога: `✅ Successfully executed commands to all hosts.`

### Экранирование путей (`internal/script`)

Один хелпер на все места, где путь попадает в shell (пролог `cd`, `mkdir -p`, `tar -C`):

- `~/x` и `~` → `"$HOME"/'x'` (тильда в одинарных кавычках не раскрывается, поэтому `$HOME`
  подставляется двойными кавычками, а остаток квотируется как обычно);
- `~user/x` → ошибка конфигурации (код 2);
- прочее → `'…'` с заменой `'` на `'\''`;
- пустой путь → ошибка конфигурации.

### Новая семантика: `workdir` и `create_workdir`

- `workdir` — абсолютный путь или `~/…`; пролог скрипта: `mkdir -p <q> && cd <q> || exit 1`.
- `create_workdir` (bool, по умолчанию false) — включает `mkdir -p` в прологе. При заданном `files`
  каталог создаётся всегда, даже без флага: распаковка в несуществующий каталог невозможна.
- Пролог идёт первым и не зависит от `script_stop`: без каталога дальше идти нельзя.

### Новая семантика: `files`

- `files` — список локальных путей и glob-шаблонов, по одному на строку (перевод строки, а не
  запятая: пробелы в путях встречаются, запятые — нет).
- Разрешение относительно `GITHUB_WORKSPACE`, если переменная задана, иначе относительно текущего
  каталога процесса; абсолютные пути допустимы. `filepath.Glob` не поддерживает `**`.
- Несовпавший шаблон — ошибка конфигурации (код 2), а не молчаливый пропуск: иначе деплой «проходит»,
  не скопировав ничего.
- Копируется каждый совпавший элемент целиком, со своим basename (как `cp -r`): файл → файл, каталог →
  каталог с содержимым. `files: dist/*` даёт элементы `dist/` внутри `workdir`.
- Крайние случаи (фиксируем поведение, а не оставляем на волю случая):
  - симлинк на верхнем уровне списка разыменовывается, симлинки внутри каталогов копируются как
    симлинки;
  - FIFO, сокеты и устройства пропускаются с предупреждением;
  - дубликаты basename из разных шаблонов: побеждает последний;
  - ошибка чтения локального файла — код 1 (это уже выполнение, а не конфигурация);
  - загрузка не атомарна: при обрыве на хосте может остаться частично распакованный набор, это
    документируется.
- Механика: локально `archive/tar` (`FormatGNU`, сохраняются режимы и симлинки) → `session.Stdin`
  сессии `mkdir -p <q> && tar -C <q> -xf -`; закрыть stdin → `session.Wait()`; stderr хоста — в наш
  stderr. Без `--same-owner`/`--no-same-owner`: флаги не переносимы между GNU tar и busybox.
- Порядок на каждом хосте: connect → (аплоад, если задан `files`) → скрипт. Ошибка аплоада
  останавливает хост до запуска скрипта.

### Бюджет времени на хост

`command_timeout` — общий дедлайн на подготовку и выполнение хоста, а не отдельный на шаг:

- `deadline := time.Now().Add(commandTimeout)` при старте хоста;
- аплоад: `time.AfterFunc(time.Until(deadline), client.Close)` — закрытие клиента разблокирует
  `io.Copy`/`Wait`; ошибка ввода-вывода при сработавшем таймере классифицируется как timeout;
- скрипт: `remaining := time.Until(deadline)`, при `remaining <= 0` — сразу timeout, иначе чтение
  ограничено `remaining`;
- причина таймаута всегда попадает в текст ошибки (`upload timeout` / `command timeout`).

### `capture_stdout` и код возврата

`бинарник | tee -a $GITHUB_OUTPUT` в POSIX sh теряет код возврата бинаря (нет `PIPESTATUS`), поэтому:

- `entrypoint.sh` при `capture_stdout: true` выставляет `SSH_ACTION_STDOUT_FILE=<tmp>` и запускает
  бинарь напрямую, с наследованием stdout (лог остаётся живым и потоковым);
- бинарь сам дублирует удалённый stdout в этот файл (внутренняя переменная, не вход);
- после выхода `entrypoint.sh` пишет содержимое файла в `$GITHUB_OUTPUT` между строками со случайным
  разделителем, удаляет файл и завершается с сохранённым кодом бинаря.

### Доставка и версионирование

- `make dist`: `CGO_ENABLED=0`, `-trimpath -ldflags "-s -w -X main.version=$VER"`, `gzip -9n`, пишет
  `dist/checksums.txt`. `VER ?= $(shell git describe --tags --abbrev=0 2>/dev/null || echo dev)`.
  Тулчейн фиксируется директивой `toolchain` в `go.mod`.
- Релиз: `make dist` → коммит `dist/` → тег `vX.Y.Z` → push. CI на теге падает, если версия внутри
  закоммиченного бинаря не совпадает с тегом; CI на push/PR пересобирает `dist` и падает при
  расхождении `checksums.txt` с закоммиченным (иначе устаревший бинарь уедет в релиз незамеченным).
- `entrypoint.sh` — POSIX sh (в alpine/golang-образах нет bash), `shell: sh` в composite, `set -eu`,
  распаковка в `${RUNNER_TEMP:-${TMPDIR:-/tmp}}/ssh-action-$arch/ssh-action` (версия в путь не входит:
  `RUNNER_TEMP` живёт один джоб), повторная распаковка пропускается, если файл уже есть.
- `dist/*.gz` помечены `binary` в `.gitattributes`.

### Известные отклонения от upstream (сознательные)

1. Выпадают входы `version` и `curl_insecure`.
2. Добавлены `workdir`, `create_workdir`, `files` и `script_stop`.
3. Служебные сообщения action'а идут в stderr; `capture_stdout` чистый.
4. Пропущенная переменная из `envs` логируется предупреждением.
5. Ошибки конфигурации дают код `2`, а не `1`.
6. Свой цикл exec на одном соединении вместо `Stream()` (второе рукопожатие и PTY-конфликт).
7. Нет keepalive и обработки SIGTERM: при отмене джоба сессии закрываются раннером. В README — в
   ограничениях.

## What Goes Where

- **Implementation Steps** (`[ ]`): код, тесты, workflow'ы, документация в этом репозитории.
- **Post-Completion**: миграция реальных пайплайнов в Gitea, проверка на их образах, публикация тега.

## Implementation Steps

### Task 1: Модуль и парсинг входов в Config

**Files:**
- Create: `go.mod`, `go.sum`
- Create: `internal/config/config.go`
- Create: `internal/config/config_test.go`

- [x] `go mod init github.com/jt4d-lab/ssh-action`, зафиксировать `easyssh-proxy` (точная версия), `x/crypto` и директиву `toolchain` (версии `easyssh-proxy`/`x/crypto` пинятся в Task 6 при первом импорте: Go 1.26 отклоняет неиспользуемые `require`)
- [x] тесты: дефолты (`port` 22, `protocol` tcp, `username` root, `timeout` 30s, `command_timeout` 10m, `envs_format`), явные переопределения, невалидные `protocol`/`duration`/`port`
- [x] тесты: отсутствие всех кредов, `files` без `workdir`, `create_workdir` без `workdir`, пустой скрипт с `files` и без — по одному кейсу на каждое правило валидации
- [x] реализовать `Config` со всеми полями соединения, прокси, выполнения и новыми `workdir`/`create_workdir`/`files`, `FromEnv(lookup func(string) (string, bool))` с проверками из раздела «Валидация»
- [x] реализовать принимаемые имена `cipher`/`ciphers` (оба) и запустить `go test ./...` — зелёный до Task 2

### Task 2: Список хостов и `host:port`

**Files:**
- Create: `internal/config/hosts.go`
- Create: `internal/config/hosts_test.go`

- [x] тесты: список через запятую, пробелы и пустые элементы, `host:port`, `tcp4` с портом, `tcp6` без разделения, `[::1]:2222`, дубликаты
- [x] тесты: голый IPv6-литерал при `tcp`/`tcp4` → ошибка конфигурации с внятным текстом; пустой список после очистки → ошибка
- [x] реализовать `ParseHosts(list, defaultPort, protocol)` по правилам раздела «Семантика»
- [x] реализовать ошибку «missing server host» и обогащение разобранных хостов портом
- [x] запустить `go test ./...` — зелёный до Task 3

### Task 3: Экранирование путей для shell

**Files:**
- Create: `internal/script/quote.go`
- Create: `internal/script/quote_test.go`

- [x] тесты: обычный путь, путь с пробелом, с одинарной кавычкой, с двойной кавычкой и `$`, `~`, `~/x`
- [x] реализовать `QuotePath(p string) (string, error)` по правилам раздела «Экранирование путей»
- [x] тесты: `~user/x` и пустой путь → ошибка
- [x] тесты: локальный `sh -c` с результатом хелпера действительно открывает каталог с пробелом и `~`
- [x] запустить `go test ./...` — зелёный до Task 4

### Task 4: Сборка скрипта: пролог, `envs`, `script_stop`

**Files:**
- Create: `internal/script/script.go`
- Create: `internal/script/script_test.go`

- [x] тесты: порядок пролог → `script` → `script_path`, отбрасывание пустых строк, ошибка чтения файла
- [x] тесты: пролог `mkdir -p <q> && cd <q> || exit 1` при `create_workdir` и при `files`, отсутствие `mkdir` при пустом `create_workdir`, экранирование через `QuotePath`
- [x] реализовать `Build(cfg, lookup)` → итоговый текст скрипта с прологом
- [x] тесты: апперкейс имён, подстановка в `envs_format`, shell-эскейпинг значений, пропуск отсутствующей переменной с предупреждением, префиксы `allenvs`
- [x] тесты: guard после каждой строки при `script_stop`, отсутствие guard после строки с `\`, сохранение кода возврата; прогон результата через локальный `sh -c` и запуск `go test ./...`

### Task 5: Тестовый SSH-сервер в процессе

**Files:**
- Create: `internal/sshtest/server.go`
- Create: `internal/sshtest/server_test.go`

- [x] тесты: сервер принимает пароль и публичный ключ, отклоняет неверный пароль
- [x] реализовать сервер на `x/crypto/ssh` с ed25519 host key, обработчиком exec (stdout, stderr, exit status) и приёмом stdin
- [x] тесты: exec получает stdin целиком и возвращает заданный код возврата, потоки раздельные
- [x] реализовать подключаемый tar-обработчик stdin (разбор через `archive/tar` во временный каталог) и барьер синхронизации хостов, PTY-приём
- [x] реализовать TCP-форвардер для jump-хоста (переиспользуется в `testserver`), тест: соединение через форвардер доходит до второго сервера; запустить `go test ./...`

### Task 6: Соединение и исполнение скрипта на одном хосте

**Files:**
- Create: `internal/sshx/conn.go`
- Create: `internal/sshx/exec.go`
- Create: `internal/sshx/exec_test.go`

- [x] тесты: успешный скрипт печатает stdout в stdout, stderr в stderr, построчно
- [x] реализовать `dial(cfg, host)` на `easyssh.MakeConfig.Connect()` с немедленным закрытием возвращённой сессии и передачей PTY только в нашу сессию скрипта
- [x] тесты: неверный пароль, несовпавший fingerprint, ненулевой удалённый exit status → ошибка с хостом и кодом
- [x] реализовать exec-цикл: `NewSession`, PTY по `request_pty`, `Start`, чтение stdout/stderr, дедлайн из раздела «Бюджет времени», классификация ошибок
- [x] тесты: `command_timeout` истёк → ошибка «command timeout» (а не «exit status»), клиент закрывается; запустить `go test ./...`

### Task 7: Копирование файлов на хост через tar

**Files:**
- Create: `internal/sshx/upload.go`
- Create: `internal/sshx/files.go`
- Create: `internal/sshx/upload_test.go`

- [x] тесты: разворачивание `files` (файл, каталог, glob, несколько строк) в список путей, ошибка на несовпавший шаблон, разрешение относительно `GITHUB_WORKSPACE`
- [x] реализовать сборку tar-стрима через `archive/tar` с `FormatGNU`, режимами, симлинками, пропуском не-регулярных файлов и семантикой дубликатов
- [x] тесты: путь длиннее 100 символов читается tar-обработчиком `sshtest` без потерь
- [x] реализовать аплоад: `NewSession`, `mkdir -p <q> && tar -C <q> -xf -`, stdin, закрытие, `Wait`, stderr хоста в stderr, ошибка с хостом/путём и кодом `tar`
- [x] тесты: ошибка `tar` на хосте останавливает хост до скрипта; аплоад подчиняется общему дедлайну; `request_pty: true` + `files` не ломает поток; запустить `go test ./...`

### Task 8: Мультихост: параллельно, `sync` и порядок шагов

**Files:**
- Create: `internal/sshx/hosts.go`
- Create: `internal/sshx/hosts_test.go`

- [ ] тесты: параллельность детерминированно — барьер в `sshtest` отпускает хосты только когда подключились оба; последовательная реализация тест не проходит
- [ ] реализовать параллельный запуск с `WaitGroup` и буферизованным каналом ошибок, возврат первой ошибки после завершения всех
- [ ] тесты: `sync: true` останавливается на первом сбое и не трогает следующий хост
- [ ] реализовать последовательный режим, порядок шагов хоста (connect → upload → script) и префикс `host: ` при числе хостов > 1
- [ ] тесты: без префикса при одном хосте, финальная строка-итог только при успехе; запустить `go test ./...`

### Task 9: CLI, коды выхода и debug

**Files:**
- Create: `cmd/ssh-action/main.go`
- Create: `cmd/ssh-action/main_test.go`
- Modify: `internal/config/config.go`

- [ ] тесты: коды `0`/`1`/`2` (успех, ошибка исполнения или копирования, битая конфигурация), `--version` печатает версию из ldflags
- [ ] реализовать `run(args, lookup, stdout, stderr) int` с флагами поверх env для локальной отладки
- [ ] тесты: `debug` не печатает `key`/`password`/`passphrase` и значения env, но печатает имена и список `files`
- [ ] реализовать redacted-дамп конфигурации, дублирование удалённого stdout в `SSH_ACTION_STDOUT_FILE`
- [ ] реализовать финальную строку-итог и служебные сообщения в stderr; запустить `go test ./...`

### Task 10: Composite action, `entrypoint.sh` и `make dist`

**Files:**
- Create: `action.yml`
- Create: `action/entrypoint.sh`
- Create: `.gitattributes`
- Create: `Makefile`
- Create: `tests/entrypoint_test.sh`

- [ ] `action.yml`: все входы/дефолты/описания (включая `workdir`, `create_workdir`, `files`), выход `stdout`, один шаг с `shell: sh`, маппинг в `INPUT_*`
- [ ] `tests/entrypoint_test.sh`: арка `amd64`/`arm64`, неизвестная арка → код 2, распаковка gz, повторный запуск не перераспаковывает, путь собирается без неинициализированных переменных
- [ ] `tests/entrypoint_test.sh`: `capture_stdout` — выход в `GITHUB_OUTPUT` между случайными разделителями, файл удаляется, код возврата бинаря сохраняется (падающая команда → шаг падает)
- [ ] реализовать `entrypoint.sh` на POSIX sh (`set -eu`): арка, распаковка в `RUNNER_TEMP`, `exec`, `capture_stdout` по разделу «`capture_stdout` и код возврата»
- [ ] `Makefile`: `build`, `test`, `lint`, `dist` (`CGO_ENABLED=0`, две арки, `gzip -9n`, `dist/checksums.txt`); `sh tests/entrypoint_test.sh` + `shellcheck` + `go test ./...` зелёные

### Task 11: Собрать и закоммитить `dist/`

**Files:**
- Create: `dist/linux-amd64.gz`, `dist/linux-arm64.gz`, `dist/checksums.txt`

- [ ] `make dist` и коммит артефактов: без этого шага Task 12 и e2e запускать нечего
- [ ] `tests/dist_test.sh`: распакованный бинарь печатает версию из `VER`, `ldd` подтверждает статическую сборку
- [ ] проверить, что `dist/checksums.txt` соответствует файлам (`sha256sum -c` в `tests/dist_test.sh`)
- [ ] зафиксировать в `Makefile` цель `release-check` (пересборка + сверка checksums) — используется в Task 13
- [ ] запустить `sh tests/dist_test.sh`; при любой последующей правке исходников пересобрать `dist/`

### Task 12: E2E action'а в двух форжах

**Files:**
- Create: `testserver/main.go`
- Create: `.github/workflows/ci.yml`
- Create: `.gitea/workflows/ci.yml`

- [ ] `testserver/main.go`: запуск `sshtest` с флагами (адрес, пароль, ключ, jump-хост, настоящий `sh`/`tar`), печать host key
- [ ] workflow: job `go test ./...` + `go vet` + `shellcheck` + `make release-check`
- [ ] workflow: e2e с `uses: ./` — password, key, fingerprint, proxy через jump, `capture_stdout` (включая падающую команду), `script_stop`, multi-host на двух портах
- [ ] workflow: e2e `workdir`/`create_workdir`/`files` — копирование каталога и файла по glob, проверка содержимого и режимов на хосте, запуск скрипта уже в `workdir`, каталог с пробелом в пути
- [ ] скопировать workflow в `.gitea/workflows/ci.yml` (один канонический файл, копия без расхождений) и зафиксировать требование к раннеру: linux, `sh`, `gzip`, `tar` на хосте, без docker и node

### Task 13: Релизный workflow

**Files:**
- Create: `.github/workflows/release.yml`
- Modify: `Makefile`

- [ ] `release.yml` на тег: `make dist`, проверка что версия в бинаре равна тегу, падение при расхождении
- [ ] `release.yml`: проверка `checksums.txt` после сборки (закоммиченные файлы против пересобранных)
- [ ] тест-сценарий в `tests/dist_test.sh` для режима `release-check` (подмена версии ловится)
- [ ] задокументировать порядок релиза в `Makefile` (`dist`, `release-check`) и в README
- [ ] проверить прогон `make release-check` на текущем дереве — зелёный

### Task 14: README и миграция

**Files:**
- Modify: `README.md`
- Create: `LICENSE`

- [ ] README: установка, полный список входов с дефолтами, примеры (пароль, ключ, proxy, multi-host, envs, capture_stdout)
- [ ] README: примеры `workdir`, `create_workdir` и `files` (деплой артефактов: создание каталога релиза, копирование `dist/*`, затем скрипт уже в нём), включая ограничение «glob без `**`»
- [ ] README: раздел «Migrating from appleboy/ssh-action» — замена `uses:`, выпавшие `version`/`curl_insecure`, оба имени `cipher`/`ciphers`
- [ ] README: отклонения из раздела «Известные отклонения» и ограничения (linux amd64/arm64, нет keepalive/SIGTERM, неатомарная загрузка, `allenvs` отправляет `INPUT_KEY`/`INPUT_PASSWORD`/`GITHUB_TOKEN`)
- [ ] `LICENSE` MIT + attribution `drone-ssh`/`easyssh-proxy`; README: процесс релиза и как обновлять `dist/`

### Task 15: Verify acceptance criteria

- [ ] проверить, что все входы из Technical Details присутствуют в `action.yml` и доходят до `Config`
- [ ] проверить, что в рантайме нет сетевых запросов: `grep -rn "curl\|wget\|https://" action/ Makefile action.yml`
- [ ] `go test ./... -race` и `go vet ./...` зелёные; `sh tests/entrypoint_test.sh` и `sh tests/dist_test.sh` зелёные
- [ ] проверить коды выхода `0`/`1`/`2` на реальных сценариях, включая `files` без `workdir` и `~user` в `workdir`
- [ ] проверить сценарии: большой файл, путь с пробелом, `request_pty` + `files`, падающая команда с `capture_stdout`
- [ ] убедиться, что `dist/` соответствует текущему исходнику (версия и checksums)

### Task 16: [Final] Обновить документацию и закрыть план

- [ ] обновить README при отклонениях, обнаруженных в процессе
- [ ] если появились новые конвенции — зафиксировать их
- [ ] переместить план в `docs/plans/completed/` (`mkdir -p docs/plans/completed`)

## Post-Completion

*Требует ручных действий или внешних систем — без чекбоксов.*

**Manual verification**:
- прогнать action в реальном Gitea-пайплайне за VPN на их образах (проверить, что `sh` и `gzip` есть,
  что чекаут action-репы кэшируется, а на хостах есть `tar`)
- сравнить поведение с текущим `appleboy/ssh-action` на одном и том же сценарии деплоя
- проверить копирование крупного артефакта (сотни МБ) и поведение при обрыве соединения
- проверить работу на arm64-раннере, если он появится

**External system updates**:
- перевести пайплайны на новый `uses:` и удалить зависимость от `appleboy/ssh-action`
- решить, нужен ли зеркальный репозиторий во внутренней Gitea, если чекаут с github.com остаётся медленным
- опубликовать первый тег `v1.0.0` после успешного e2e в обоих форжах
