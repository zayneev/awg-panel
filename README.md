# awgpanel

Терминальный интерфейс для серверов, установленных через [`bivlked/amneziawg-installer`](https://github.com/bivlked/amneziawg-installer) 5.20.x или 5.21.x.

`awgpanel` не заменяет `manage_amneziawg.sh`: клиенты, ключи и конфигурация остаются в AmneziaWG. Программа запускается через SSH и `sudo`, напрямую вызывает разрешённые операции upstream-скрипта и не создаёт web-сервер. Опциональная WARP-маршрутизация устанавливается только с флагом `--with-routing` и включается отдельной явной командой.

## Возможности

- полноэкранный список клиентов со статусом handshake и статистикой RX/TX;
- создание постоянных и временных клиентов, опциональный PSK;
- изменение DNS, Endpoint, AllowedIPs и PersistentKeepalive;
- два QR-кода: `vpn://` для Amnezia Client и `.conf` для AmneziaWG;
- показ существующего `vpn://` и безопасная передача существующего `.conf` через SSH;
- удаление клиентов, перезапуск AWG и создание backup;
- подкоманды и JSON-вывод для автоматизации.
- опциональная маршрутизация выбранных доменов через отдельный WARP-outbound Xray; весь остальной AWG-трафик идёт напрямую.

## Сборка

Требуется Go 1.26+.

```bash
go mod download
make test
make build VERSION=0.3.0
```

Linux-релизы без CGO:

```bash
make release VERSION=0.3.0
```

## Установка на VPS

На VPS с уже установленной AmneziaWG 5.20.x или 5.21.x запустите интерактивный мастер:

```bash
curl -fsSL https://github.com/zayneev/awg-panel/releases/latest/download/install.sh | sudo bash
```

Мастер определит архитектуру amd64/arm64, скачает последний релиз и проверит бинарник по `SHA256SUMS`. Он проверит совместимость AmneziaWG, предложит routing-компоненты, покажет все изменения и запросит подтверждение. Существующие `/etc/awgpanel/config.json`, routing-правила и WARP-секреты всегда сохраняются.

Установка конкретной версии без вопросов:

```bash
curl -fsSL https://github.com/zayneev/awg-panel/releases/download/v0.3.0/install.sh |
  sudo bash -s -- --version=0.3.0 --non-interactive
```

При обновлении установщик сохраняет заменяемые файлы во временную резервную копию и автоматически восстанавливает их при неуспешной проверке. Если routing активен, перехват кратковременно снимается, службы безопасно обновляются и возвращаются в исходное состояние; при ошибке запуска восстанавливается предыдущая версия.

Без routing устанавливается только `/usr/local/bin/awgpanel`: фоновые службы, пользователи, сокеты и TCP-порты не создаются.

Для установки выключенных routing-компонентов на Debian 12 или Ubuntu 22.04/24.04 (amd64/arm64):

```bash
sudo ./install.sh --with-routing --non-interactive
```

Установщик загружает отдельный Xray v26.7.11, проверяет зафиксированный SHA256 архива, устанавливает `geosite.dat` и две выключенные systemd-службы. Он не запускает Xray/DNS и не создаёт nftables-правила. Для полностью офлайн-установки заранее скопируйте Linux-бинарник панели, официальный архив Xray и сам установщик:

```bash
sudo ./install.sh --binary=./awgpanel-linux-amd64 \
  --with-routing --xray-archive=./Xray-linux-64.zip
```

При переходе со старой web-панели прежние службы останавливаются и удаляются. База авторизации и TOTP-ключ по умолчанию сохраняются; удалить их можно явным флагом:

```bash
sudo ./install.sh --purge-legacy-web-state --non-interactive
```

Все параметры можно посмотреть через `./install.sh --help`. При наличии TTY запуск без параметров открывает мастер; любой прежний сценарий с явными параметрами остаётся неинтерактивным, если не добавить `--interactive`.

## Интерактивный режим

```bash
ssh -t user@server.example 'sudo awgpanel'
```

Основные клавиши:

- `↑`/`↓` или `j`/`k` — выбрать клиента;
- `Enter` — открыть карточку;
- `A` — добавить клиента;
- `Q` — выбрать QR для `vpn://` или `.conf`;
- `U` — показать `vpn://`;
- `C` — показать команду сохранения `.conf`;
- `E` — изменить клиента, `D` — удалить;
- `B` — backup, `S` — restart, `R` — обновить;
- `W` — раздел «Обзор / Правила / WARP»;
- `G` во вкладке WARP — зарегистрировать отдельное WARP-устройство с явным подтверждением условий Cloudflare;
- `I` — импортировать существующий wg-quick `.conf`, `T` — проверить WARP, `F` — удалить локальные WARP credentials;
- `Esc` — назад или выход.

## Подкоманды

```bash
sudo awgpanel status
sudo awgpanel clients list
sudo awgpanel clients list --json
sudo awgpanel clients add phone --psk
sudo awgpanel clients add guest --expires 7d
sudo awgpanel clients show phone
sudo awgpanel clients edit phone --field DNS --value 1.1.1.1
sudo awgpanel clients qr phone --type vpn
sudo awgpanel clients qr phone --type config
sudo awgpanel clients uri phone
sudo awgpanel clients delete phone
sudo awgpanel backup
sudo awgpanel restart
```

## Маршрутизация доменов через WARP

Архитектура изолирована от рабочего AWG и 3x-ui:

- DNS-классификатор принимает только перенаправленные с `awg0` UDP/TCP-запросы на порту 1053, возвращает исходный ответ и наполняет nftables sets по TTL (30–3600 секунд);
- только реальные A/AAAA-адреса совпавших правил попадают в Xray TProxy на порту 17890;
- Xray использует собственный WireGuard outbound с `noKernelTun: true`; его конфиг, регистрация и порты не связаны с 3x-ui;
- весь остальной трафик остаётся в обычном forwarding Linux и не проходит через Xray;
- остановка DNS или Xray удаляет только таблицу `inet awgpanel` и policy rules awgpanel, включая direct-fallback;
- при живом Xray, но недоступном WARP, совпавшие WARP-направления блокируются, а direct-трафик продолжает работать.

Полную первичную настройку можно выполнить в TUI: `W` → вкладка `WARP`. Клавиша `G` регистрирует новое устройство с отдельным подтверждением условий Cloudflare, а `I` импортирует существующий wg-quick `.conf` по абсолютному пути на сервере. В обоих случаях панель сохраняет секреты с правами `0600` и автоматически выполняет health-check. `F` удаляет только локальные WARP credentials и сгенерированный Xray-конфиг, сохраняя routing-правила. Регистрация, импорт и удаление доступны при выключенном routing. Те же сценарии доступны через CLI:

```bash
sudo awgpanel routing warp register --accept-tos
sudo awgpanel routing warp test
sudo awgpanel routing rules add cloudflare \
  --domain cloudflare.com --domain cloudflare-dns.com \
  --outbound warp --scope global --priority 100
sudo awgpanel routing check
sudo awgpanel routing enable --yes
sudo awgpanel routing status
```

Импорт существующего стандартного wg-quick-конфига вместо регистрации (в TUI — клавиша `I`):

```bash
sudo awgpanel routing warp import /root/warp.conf
```

Правило для отдельных AWG-клиентов и direct-исключение:

```bash
sudo awgpanel routing rules add video-phone \
  --domain example.com --geosite youtube \
  --scope clients --client phone --outbound warp --priority 100
sudo awgpanel routing rules add direct-phone \
  --domain auth.example.com \
  --scope clients --client phone --outbound direct --priority 10
sudo awgpanel routing apply --yes
```

Обычный домен автоматически включает поддомены. Домены приводятся к lowercase/IDNA. Клиентское правило с отсутствующим клиентом не расширяется до global: оно считается ошибочным, а после удаления последнего клиента автоматически выключается.

Полный CLI:

```bash
sudo awgpanel routing status --json
sudo awgpanel routing check --json
sudo awgpanel routing enable --yes
sudo awgpanel routing disable --yes
sudo awgpanel routing apply --yes
sudo awgpanel routing emergency-disable --yes
sudo awgpanel routing warp register --accept-tos
sudo awgpanel routing warp import FILE
sudo awgpanel routing warp test --json
sudo awgpanel routing warp forget --yes
sudo awgpanel routing rules list --json
sudo awgpanel routing rules add ID --domain DOMAIN --outbound warp
sudo awgpanel routing rules set ID --priority 10
sudo awgpanel routing rules enable ID
sudo awgpanel routing rules disable ID
sudo awgpanel routing rules delete ID --yes
```

Перед `enable` выполняются dry-run, проверка портов/policy table/Xray/geosite и WARP health-check. На время операции ставится 120-секундный аварийный rollback. `emergency-disable` не читает `routing.json`, не восстанавливает общий ruleset и удаляет только объекты awgpanel.

Секреты WARP находятся в `/etc/awgpanel/routing/warp.json` с правами `0600`; private key, token, license и пароль health-proxy не выводятся в TUI, status JSON или логи.

Ограничения первой версии: клиентский DoH скрывает DNS-запрос от классификатора; ECH может скрывать имя от других наблюдателей, но классификация здесь основана именно на DNS; общий CDN-IP маршрутизируется целиком до истечения TTL. Regexp-правила, управление WARP+ license, ротация IP и балансировка не реализованы.

При простое добавляются два небольших процесса (DNS proxy и Xray). Direct-трафик не несёт Xray-нагрузки. Фактические CPU/RAM и пропускная способность WARP зависят от CPU VPS, сети и MTU; процедура измерения приведена в [VPS_ACCEPTANCE.md](docs/VPS_ACCEPTANCE.md).

Полная справка:

```bash
awgpanel --help
awgpanel completion bash
```

## Сохранение `.conf` на локальный компьютер

Файл клиента уже находится на VPS. `awgpanel` не генерирует его повторно, а передаёт существующие байты через зашифрованный SSH-канал:

```bash
umask 077
ssh -T user@server.example \
  'sudo awgpanel clients config phone' > phone.conf
```

Команда отказывается печатать `.conf` прямо в интерактивный терминал, чтобы приватный ключ случайно не попал в scrollback. Диагностика выводится в stderr и не смешивается с содержимым файла.

## Конфигурация

По умолчанию используются пути upstream-инсталлятора:

- `/root/awg/manage_amneziawg.sh`;
- `/root/awg/awg_common.sh`;
- `/root/awg`;
- `/etc/amnezia/amneziawg/awg0.conf`.

Их можно переопределить в `/etc/awgpanel/config.json` или передать другой файл через глобальный флаг `--config`.

Restore, установка/переустановка AWG, `repair-module`, `diagnose` и глобальная конфигурация намеренно не входят в текущую версию.

Проект не аффилирован с Amnezia VPN или автором upstream installer.
