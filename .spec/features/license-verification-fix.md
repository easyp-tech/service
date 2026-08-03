# ТЗ: починка офлайн-проверки лицензии

**Статус: сделано.** Ветка `feat/plugin-migration-cli`.
Затрагивает: `internal/license/`, `cmd/easyp-svc/`, `internal/config/`,
`charts/easyp-service/`, `.github/workflows/go.yml`, `.spec/`.

З9 закрыт. Приватная половина `2e80e973…` действительно была потеряна, ключ
пересоздан в реестре под тем же kid (ему никто никогда не доверял), новая
публичная половина `81322461…` лежит в дефолтах чарта. Токен, выпущенный
реестром, проверен против живого сервиса: `licence accepted, tier=enterprise`.

**Выпуск лицензий сюда не относится.** Он живёт в `easyp-tech/licenses`. Была
попытка добавить `easyp-svc license keygen/issue` ради теста «выпущенное
проверяется» — это оказалось дублем уже существующего `easyp-license` и было
удалено. Формат согласуется тестами с обеих сторон, а не общим кодом;
подробности в `.spec/AUTH.md`, раздел Issuing.

Живой прогон вскрыл два дефекта, которых в списке ниже не было и которых не
показали ни тесты, ни линтер:

- `Manager.refresh` (`internal/license/manager.go:113`) писал INFO на каждом
  тике — та же проблема, что №8, этажом выше. Исправлено там же: Info при смене
  тарифа, Debug иначе. Покрыто `TestRefreshLogsOnlyOnChange`.
- Предупреждения об отвергнутом токене повторялись каждый тик. Сведены к тому же
  правилу «по изменению»: сломанная лицензия — это состояние, а состояние живёт
  в метрике тарифа, которая экспортируется непрерывно. Покрыто
  `TestRejectionIsReportedOnlyOnce`.

---

## 1. Что сломано

Проверка подписи PASETO написана (`internal/license/paseto_client.go`), но не
работает. Ниже — только подтверждённое, с указанием места.

| # | Дефект | Место | Следствие |
|---|--------|-------|-----------|
| 1 | Новый клиент не подключён | `cmd/easyp-svc/start.go:301` — `license.NewMockLicenseClient(...)` | Сервис принимает любой непустой токен за Enterprise. Весь новый код мёртв. |
| 2 | Грейс-период недостижим | `paseto_client.go:80` — `paseto.NewParser()` содержит правило `NotExpired()` (см. `go-paseto@v1.6.0/parser.go:17`) | Просроченный токен падает внутри `ParseV4Public`, до ручной логики строк 102–144 управление не доходит. Проверено: токен с `exp` 10 дней назад и `grace_days: 14` даёт `community`. |
| 3 | Тест грейса даёт ложную зелень | `paseto_client_test.go:87` | Тест двигает часы **клиента**, а `NotExpired()` смотрит на настоящее время. Токен (`exp = 2027-08-01`) в реальном времени ещё жив, поэтому библиотека пропускает его, а ручная логика считает по фальшивым часам. Тест не может упасть по настоящей причине. Побочно: 2027-08-01 он сломается сам. |
| 4 | Легаси-ключ не подходит ни к одному токену | `client.go:32` кладёт ключ под kid `"default"`, `paseto_client.go:165` читает kid из footer (`2026-08`) | Установка с одним `LICENSE_PUBLIC_KEY` молча уходит в community. |
| 5 | Чарт объявляет настройку, которой нет | `values.yaml:141-143` vs `templates/deployment.yaml` (ни одного упоминания license) | `config.license.publicKeys` не рендерится ни во что. |
| 6 | Публичный ключ без известной приватной половины | `values.yaml:143` — `2e80e973…` | Ключ, судя по всему, из удалённого `licensing-service`, где приватная половина лежала в Postgres. **Требует проверки прежде, чем на него положатся.** |
| 7 | Диагностика вводит в заблуждение | `paseto_client.go:86` | Просроченный токен логируется как «signature or format verification failed» — оператор пойдёт искать несовпадение ключей. |
| 8 | Info на каждой валидации | `paseto_client.go:139` | `ValidateLicense` зовётся раз в `license.cache_ttl` (дефолт 5m) → ~288 строк «licence token accepted successfully» в сутки. |
| 9 | Мёртвый код | `internal/core/domain.go` — `ParseFeature` | При тарифе на токене фичи не парсятся; функция не вызывается ниоткуда. Возвращает `Feature(-1)`. |
| 10 | `go.mod` не прибран | `aidanwoods.dev/go-paseto v1.6.0 // indirect` | Пакет импортируется напрямую. `go mod tidy` даст диф. |
| 11 | 19 замечаний линтера | новый код | `cyclop`, `err113`×3, `ireturn`, `mnd`×2, `modernize`, `nlreturn`×3, `noinlineerr`, `paralleltest`×4, `sloglint`, `varnamelen`×2. Планка репозитория — ноль. |

Что сделано правильно и менять не надо: тариф на токене, а фичи и лимиты из
кода; `kid` читается из footer до проверки подписи и служит только выбором
ключа; любой сбой ведёт в community, а не в отказ обслуживания; допуск на
расхождение часов.

---

## 2. Решения

Приняты до задач, потому что определяют их форму.

**Р1. `MockLicenseClient` удаляется.**
Не «заменяется вызов», а удаляется тип. Заглушку, которой нет, нельзя забыть
выключить — а забыли именно её. Поведение «ключей не настроено → community»
уже реализовано в `PasetoLicenseClient` (строки 60–65), отдельный тип для этого
не нужен. Удаляются `client.go` целиком и `client_test.go`.

**Р2. Битый ключ — отказ старта. Отсутствующий ключ — community.**
Невалидный hex это опечатка оператора; тихая деградация в community её прячет.
Отсутствие ключа — легитимная конфигурация community-установки.

**Р3. Клиент строится в `runStart`, не в `initApp`.**
`resolveLicense` уже вызывается в `runStart` (`start.go:145`). Туда же переносится
построение клиента, чтобы ошибка Р2 могла остановить старт: `initApp` ошибок не
возвращает. В `initApp` передаётся готовый `core.LicenseClient` вместо
`licenseCredentials` — заодно `initApp` становится тестируемым со стабом.

**Р4. Один источник времени.**
`paseto.NewParserWithoutExpiryCheck()` + правила `IssuedBy`/`ForAudience`.
Все проверки `nbf`/`exp`/грейса — ручные, в одном месте, по `c.clock()`.
Два источника времени в одной проверке — это и есть дефект №2.

**Р5. `SetClock` заменяется неэкспортируемой функциональной опцией.**
Экспортированный сеттер, мутирующий объект проверки лицензии после
конструктора, — лишняя поверхность. `withClock(f)` доступна только тестам пакета.

**Р6. Легаси-ключ применяется при любом kid.**
Отдельное поле `fallbackKey`, а не запись в карту под выдуманным kid. Подпись
всё равно обязана сойтись с этим ключом, поэтому это безопасно.

**Р7. Логирование по смене состояния, не по факту проверки.**

---

## 3. Задачи

### З1. `internal/license/paseto_client.go`

Конструктор:

```go
type option func(*PasetoLicenseClient)

// withClock подменяет часы. Только для тестов пакета.
func withClock(clock func() time.Time) option {
	return func(c *PasetoLicenseClient) { c.clock = clock }
}

// NewPasetoLicenseClient строит клиента по карте kid -> hex-ключ.
// fallbackKey (может быть пустым) применяется, когда kid не найден в карте:
// это путь для установок с единственным LICENSE_PUBLIC_KEY.
func NewPasetoLicenseClient(
	token string,
	publicKeysHex map[string]string,
	fallbackKeyHex string,
	logger *slog.Logger,
	opts ...option,
) (*PasetoLicenseClient, error)
```

Правки в `ValidateLicense`:

1. `paseto.NewParser()` → `paseto.NewParserWithoutExpiryCheck()`.
2. Выбор ключа: `publicKeys[kid]`, при промахе — `fallbackKey`, при его
   отсутствии — community с Warn, содержащим и `kid`, и список настроенных kid.
3. Если извлечение kid не удалось, но `fallbackKey` задан — использовать его
   (токен без footer это легитимный старый формат).
4. `grace_days` читать явно, с комментарием, что отсутствие claim = 0 дней.
5. Сообщения об ошибках разнести: подпись/формат, просрочен сверх грейса, ещё не
   вступил в силу, неизвестный tier — четыре разных текста (дефект №7).
6. `clockSkew` — именованная константа пакета, а не литерал в теле (`mnd`).
7. Логирование (дефект №8): хранить в клиенте последнее сообщённое состояние
   (`tier` + `exp` + признак грейса); `Info` писать только при его изменении.
   Пребывание в грейсе логировать `Warn` при входе в грейс, а не каждые 5 минут.

Разбить функцию: `ValidateLicense` → `resolveKey`, `checkValidity`,
`claimsFor`. Сейчас она не пройдёт `cyclop` (10) и близка к `funlen` (60).

### З2. `internal/license/client.go` — удалить

Вместе с `client_test.go`. Единственный конструктор — `NewPasetoLicenseClient`.

### З3. `cmd/easyp-svc/license.go`

- `licenseCredentials` получает поле `publicKeys map[string]string`.
- `resolveLicensePublicKeys(cfg)`: `cfg.PublicKeys`, иначе разбор
  `LICENSE_PUBLIC_KEYS` из окружения в формате `kid:hex,kid:hex`.
  **Фолбэк на окружение обязателен:** путь `--cfg` (им пользуется
  docker-compose, `docker-compose.yml:207`) обходит envconfig целиком.
  Формат должен совпадать с тем, что понимает envconfig: разделитель пар `,`,
  разделитель ключ/значение `:` (`go-envconfig@v1.3.0`, `DefaultDelimiter`,
  `DefaultSeparator`).
- Новая `buildLicenseClient(cfg config.LicenseConfig, log *slog.Logger) (core.LicenseClient, error)` —
  собирает креды и клиента, возвращает ошибку при невалидном ключе (Р2).

### З4. `cmd/easyp-svc/start.go`

- `runStart`: после `resolveLicense` вызвать `buildLicenseClient`; ошибка —
  завершение старта с внятным сообщением, а не Warn.
- `initApp`: параметр `licenseCreds licenseCredentials` → `licenseClient core.LicenseClient`.
- Строка 301 удаляется.

### З5. `internal/config/config.go`

- Вернуть развёрнутый комментарий к `PublicKey`/`PublicKeys` про то, что якорь
  доверия задаётся конфигурацией, а не сборкой (он был вырезан).
- В `Validate()`: каждое значение `PublicKeys` — 64 hex-символа; kid не пустой
  и не содержит `:` и `,` (иначе кодировка в env неоднозначна).

### З6. `internal/core/domain.go`

Удалить `ParseFeature`.

### З7. Чарт

`templates/_helpers.tpl`:

```gotemplate
{{- define "easyp-service.licensePublicKeys" -}}
{{- $pairs := list -}}
{{- range $kid, $hex := .Values.config.license.publicKeys -}}
  {{- if not (regexMatch "^[0-9a-fA-F]{64}$" (trim $hex)) -}}
    {{- fail (printf "config.license.publicKeys[%s]: expected 64 hex characters, got %q" $kid $hex) -}}
  {{- end -}}
  {{- if or (contains ":" $kid) (contains "," $kid) -}}
    {{- fail (printf "config.license.publicKeys: key id %q must not contain ':' or ','" $kid) -}}
  {{- end -}}
  {{- $pairs = append $pairs (printf "%s:%s" $kid (trim $hex)) -}}
{{- end -}}
{{- join "," $pairs -}}
{{- end -}}
```

`templates/deployment.yaml`, в блоке `env`:

```gotemplate
{{- with .Values.config.license.publicKeys }}
- name: LICENSE_PUBLIC_KEYS
  value: {{ include "easyp-service.licensePublicKeys" $ | quote }}
{{- end }}
```

`values.yaml`: **убрать `2e80e973…`**, оставить `publicKeys: {}` с комментарием,
откуда ключ берётся (публичная половина из `easyp-tech/licenses`, `keys/`).
Класть в дефолты чарта ключ, приватной половиной которого никто не владеет,
хуже, чем не класть ничего: он выглядит рабочим.

`README.md` чарта: строку про `LICENSE_PUBLIC_KEY` дополнить `LICENSE_PUBLIC_KEYS`
и форматом `kid:hex,kid:hex`.

### З8. Документация

- `AGENTS.md:116` — утверждение «MockLicenseClient always returns Enterprise»
  становится неверным.
- `.spec/AUTH.md:78` — «Current implementation: MockLicenseClient».
- `.spec/PACKAGES.md:111` — `client.go` больше нет.
- `README.md:445-450`, `config.yml:52`, `Taskfile.yml:11` — упомянуть
  `LICENSE_PUBLIC_KEYS`.
- `licenses/README.md` в соседнем репозитории — снять «блокирующую зависимость».

### З9. Внешнее, вне кода

Выяснить судьбу приватной половины `2e80e973…`. Если утрачена — сгенерировать
пару заново по `licenses/docs/setup.md` и выпустить первый ключ `2026-08`
оттуда. **Это блокирует релиз, но не мерж этого ТЗ.**

---

## 4. Тесты

Правило для всех: **никаких абсолютных дат.** Все сроки строятся относительно
`time.Now()` в момент запуска теста. Именно абсолютные даты позволили дефекту №3
пройти незамеченным, и они же сломают текущий набор 2027-08-01.

Хелпер в `paseto_client_test.go`:

```go
type tokenSpec struct {
	kid        string        // footer kid; "" => footer не ставится
	rawFooter  []byte        // если задан, вытесняет kid — для кривых footer
	issuer     string        // "" => "easyp.tech"
	audience   string        // "" => "easyp-service"
	tier       string        // "" => "enterprise"; "-" => claim не ставится
	customer   string
	notBefore  time.Duration // смещение от now, отрицательное = в прошлом
	expiration time.Duration // смещение от now
	graceDays  int
	omitGrace  bool
	signWith   paseto.V4AsymmetricSecretKey
}

func mint(t *testing.T, spec tokenSpec) string
```

### 4.1 Срок действия и грейс

Ни один из этих тестов не подменяет часы. Половина падает на текущем коде — это
и есть цель.

| Имя | Токен | Ожидание | Сейчас |
|---|---|---|---|
| `expiry/valid` | exp = now+30d, grace 14 | enterprise | ✅ |
| `expiry/within_grace` | **exp = now−10d**, grace 14 | **enterprise** | ❌ |
| `expiry/at_grace_boundary` | exp = now−14d−30s, grace 14 | enterprise (в пределах skew) | ❌ |
| `expiry/past_grace` | exp = now−20d, grace 14 | community | ✅ по неверной причине |
| `expiry/grace_zero` | exp = now−1h, grace 0 | community | ✅ по неверной причине |
| `expiry/grace_claim_absent` | exp = now−1h, без `grace_days` | community | ✅ по неверной причине |
| `expiry/exp_claim_absent` | без `exp` | community | |
| `nbf/future` | nbf = now+2d, exp = now+30d | community | |
| `nbf/within_skew` | nbf = now+30s | enterprise | |

«По неверной причине» означает: тест зелёный, но срабатывает правило библиотеки,
а не проверяемая логика. После З1 они начнут проверять то, что заявляют.

### 4.2 Ключи и kid

| Имя | Условие | Ожидание |
|---|---|---|
| `kid/selects_matching_key` | подписан A, kid=X, конфиг `{X:A, Y:B}` | enterprise |
| `kid/unknown` | kid=Z, конфиг `{X:A}`, fallback пуст | community |
| **`kid/key_confusion`** | подписан **B**, footer заявляет kid=**X** (X→A) | community |
| `kid/footer_absent` | footer не ставится, fallback пуст | community |
| `kid/footer_not_json` | `rawFooter: []byte("not-json")` | community |
| `kid/footer_empty_kid` | `{"kid":""}` | community |
| `fallback/any_kid` | конфиг: карта пуста, fallback=A; токен kid=`2026-08` подписан A | **enterprise** |
| `fallback/wrong_signer` | то же, токен подписан B | community |
| `fallback/used_when_kid_unknown` | карта `{X:A}`, fallback=B, токен kid=Z подписан B | enterprise |

`kid/key_confusion` — главный тест безопасности в наборе. Footer подписан вместе
с телом, но выбор ключа по нему делается **до** проверки подписи; тест
фиксирует, что подмена kid не даёт принять чужую подпись.

### 4.3 Подпись и содержимое

| Имя | Условие | Ожидание |
|---|---|---|
| `signature/tampered` | изменённые байты тела | community |
| `signature/truncated` | обрезанный токен | community |
| `issuer/foreign` | iss=`evil.tech` | community |
| `audience/foreign` | aud=`other-service` | community |
| `tier/community` | tier=`community`, срок годен | community |
| `tier/absent` | claim не ставится | community |
| `tier/unknown` | tier=`enterprise-plus` | community |
| `claims/enterprise_set` | валидный enterprise | ровно 8 фич, `MaxWorkers == -1`, `MaxPlugins == -1` |

### 4.4 Конструктор

| Имя | Условие | Ожидание |
|---|---|---|
| `ctor/bad_hex` | ключ `zzzz…` | ошибка с указанием kid |
| `ctor/short_hex` | 32 hex-символа | ошибка |
| `ctor/whitespace_tolerated` | ключ с `\n` по краям | ок |
| `ctor/empty_token` | token = `""` | community, ключи не трогаются |
| `ctor/no_keys_with_token` | ключей нет, токен есть | community |

### 4.5 Точка сборки — `cmd/easyp-svc`

Тесты, которых не было и отсутствие которых и есть дефект №1.

| Имя | Условие | Ожидание |
|---|---|---|
| **`buildLicenseClient/garbage_token_is_not_enterprise`** | token = `"any-string"`, настроен валидный ключ | community |
| `buildLicenseClient/valid_token_is_enterprise` | токен, подписанный ключом из конфига | enterprise |
| `buildLicenseClient/no_keys_is_community` | валидный токен, ключей нет | community |
| `buildLicenseClient/bad_key_fails_startup` | битый hex | ошибка, не community |
| `buildLicenseClient/env_fallback` | конфиг пуст, `t.Setenv("LICENSE_PUBLIC_KEYS", "a:<64hex>,b:<64hex>")` | оба ключа загружены |
| `buildLicenseClient/config_wins_over_env` | заданы и конфиг, и окружение | берётся конфиг |

Первый — прямой антирегресс на возврат заглушки: любая реализация, принимающая
произвольную строку за Enterprise, его роняет.

### 4.6 Конфигурация

| Имя | Условие | Ожидание |
|---|---|---|
| `config/public_keys_from_env` | `LICENSE_PUBLIC_KEYS="2026-08:<hex>,2026-09:<hex>"` | карта из двух записей |
| `config/public_keys_from_yaml` | `license.public_keys` в YAML | то же |
| `config/validate_rejects_short_key` | 32 hex | `Validate()` возвращает ошибку |
| `config/validate_rejects_kid_with_colon` | kid `a:b` | ошибка |

### 4.7 Чарт — `charts/easyp-service/tests/render.sh`

Скрипт на `helm template`, вызывается из CI. Проверки:

1. Без `config.license.publicKeys` в выводе нет `LICENSE_PUBLIC_KEYS`.
2. С двумя ключами рендерится ровно `value: "2026-08:<hex>,2026-09:<hex>"`
   (порядок детерминирован: Helm сортирует ключи карты).
3. Ключ не из 64 hex-символов → `helm template` завершается с ненулевым кодом и
   текстом, содержащим имя kid.
4. kid с `:` → падение.
5. Дефолтные `values.yaml` рендерятся без ошибок и **не содержат** захардкоженного
   публичного ключа.

---

## 5. Как не повторить

Каждый пункт — против конкретного дефекта выше, а не «хорошая практика вообще».

**П1. Заглушек в проде не бывает.**
Против №1. Если реализация не готова, в дереве живёт один тип, который делает
безопасную вещь (community), а не два, из которых нужно не забыть выбрать
правильный. Удаление `MockLicenseClient` — не уборка, а сам механизм защиты.

**П2. У каждой security-фичи есть тест в точке сборки.**
Против №1. Юнит-тест на `PasetoLicenseClient` был зелёным, пока сервис
раздавал Enterprise всем подряд. Тест уровня `buildLicenseClient` этого не
позволяет. Правило: проверка, от которой зависит доступ или биллинг,
тестируется там, где её конструирует прод, а не только там, где она объявлена.

**П3. Тест на негативном пути обязан быть проверен мутацией.**
Против №3. Порядок: написать тест → сломать реализацию → убедиться, что тест
покраснел → починить. Тест грейса покраснеть не мог в принципе. В чек-лист
ревью: «какой правкой этот тест ломается?» — если ответа нет, тест ничего не
охраняет.

**П4. Если у зависимости свои часы — двигать данные, а не свои часы.**
Против №3, прямая формулировка причины. Подмена `c.clock` не влияла на
`NotExpired()` внутри библиотеки. Проверять срок годности следует токеном,
который просрочен по-настоящему.

**П5. Абсолютные даты в тестах запрещены.**
Против №3 и его отложенного последствия. Всё относительно `time.Now()`.

**П6. Настройка в `values.yaml` без отрендеренного потребителя — дефект.**
Против №5. Закрывается пунктом 4.7 и шагом CI ниже.

**П7. Ключ в дефолтах чарта — только тот, чьей приватной половиной мы владеем.**
Против №6.

### Шаги CI (`.github/workflows/go.yml`)

Сейчас в CI: `build`, `vet`, `test -race`, `lint`. Добавить:

```yaml
  tidy:
    name: Tidy
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      - uses: actions/setup-go@v5
        with:
          go-version-file: go.mod
      - run: go mod tidy
      - run: git diff --exit-code go.mod go.sum

  chart:
    name: Chart
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      - uses: azure/setup-helm@v4
      - run: helm lint charts/easyp-service
      - run: charts/easyp-service/tests/render.sh
```

`tidy` — против №10. `chart` — против №5 и №6, и заодно поймал бы дефект с
рендерингом float64, найденный на первом этапе только живой установкой.

---

## 6. Критерии приёмки

1. `go build ./... && go vet ./... && go test -race ./...` — зелено.
2. `golangci-lint run` — **ноль** замечаний (сейчас 19).
3. `go mod tidy` не даёт диффа; `go-paseto` в блоке прямых зависимостей.
4. `grep -rn "MockLicenseClient" .` — пусто.
5. Тест `expiry/within_grace` зелёный, и он краснеет при возврате
   `paseto.NewParser()` — проверить руками (П3).
6. Тест `buildLicenseClient/garbage_token_is_not_enterprise` зелёный, и он
   краснеет при подстановке клиента, доверяющего токену на слово (П3).
7. `charts/easyp-service/tests/render.sh` проходит; `helm template` с дефолтными
   значениями не содержит публичного ключа.
8. Живая проверка в кластере: под с настроенным `LICENSE_PUBLIC_KEYS` и
   валидным токеном логирует принятие лицензии **один раз**, а не каждые 5 минут;
   с произвольной строкой в `LICENSE_KEY` — community; с битым hex в ключе — не
   стартует и говорит почему.
9. Документы из З8 не противоречат коду.
