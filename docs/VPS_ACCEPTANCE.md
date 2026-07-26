# Приёмка routing на тестовом VPS

Проверка выполняется на VPS со снапшотом. Не используйте первый запуск на единственном сервере без out-of-band консоли провайдера.

## Матрица установщика

Проверьте Ubuntu 22.04, Ubuntu 24.04 и Debian 12 на amd64; минимум один сценарий повторите на arm64.

1. На VPS с AmneziaWG 5.20.x запустите online-мастер из GitHub Release. Откажитесь от routing и убедитесь, что установлен только `/usr/local/bin/awgpanel`, а текущие peers, порты и nftables не изменились.
2. Повторите запуск с routing. Убедитесь, что Xray v26.7.11 и обе units установлены, но имеют состояния `inactive` и `disabled`, а таблица `inet awgpanel` отсутствует.
3. Повторите установку из локальных `install.sh`, `awgpanel-linux-*` и архива Xray при заблокированном исходящем HTTP. При заранее установленных `unzip`, `nft` и `ip` обращений к APT и сети быть не должно.
4. Измените копию `/etc/awgpanel/config.json`, повторно установите ту же версию и сравните файл побайтно. Аналогично проверьте `routing.json` и `warp.json`.
5. Включите рабочий routing и обновите панель. В момент перезапуска трафик должен перейти в direct, после проверки — вернуться в состояние `active`; enabled/active состояния units должны сохраниться.
6. Для проверки rollback подайте бинарник, который проходит `--version`, но не проходит `status --json`. Установщик должен завершиться с ошибкой, вернуть прежний бинарник, Xray/assets/units и исходные состояния служб.
7. Остановите `awg-quick@awg0` и повторите обычную установку: она должна завершиться успешно с явным предупреждением. Несовместимая версия upstream 5.21.x должна остановить preflight до изменения файлов.

## Матрица состояния

Сохраните результаты на трёх этапах: до установки, после `install.sh --with-routing` (routing выключен) и после `routing enable`:

```bash
date -Is
systemctl is-active awg-quick@awg0
awg show awg0
systemctl is-active x-ui 2>/dev/null || true
ss -lntup
nft list ruleset
ip -4 rule show
ip -6 rule show
sudo awgpanel status --json
sudo awgpanel routing status --json
```

После установки с выключенной опцией должны совпасть AWG peers/handshakes, порты 3x-ui и сетевые правила; `inet awgpanel` отсутствует, routing-службы inactive/disabled.

## Функциональные сценарии

1. Создайте global WARP-правило, client WARP-правило и более приоритетные direct-исключения.
2. С двух AWG-клиентов проверьте A/AAAA, HTTP/HTTPS и Cloudflare trace для direct- и WARP-доменов.
3. Убедитесь через `nft list table inet awgpanel`, что sets получают timeout из DNS, а direct counters не проходят через TProxy.
4. Остановите WARP-доступ без остановки Xray: WARP-домены должны перестать отвечать, direct остаётся доступным.
5. Завершите Xray и отдельно DNS-процесс: таблица `inet awgpanel` должна исчезнуть, состояние — `degraded_direct`, весь трафик доступен напрямую.
6. После успешного systemd restart и WARP health-check таблица должна примениться повторно.
7. Перезагрузите VPS и повторите проверки AWG, 3x-ui, IPv4/IPv6 и правил.
8. Повредите копию `routing.json` и выполните `routing emergency-disable --yes`: удаляются только `inet awgpanel`, fwmark `0xA61` и table `1061`.

## Network namespace integration test

На Debian/Ubuntu с root, `ip` и `nft`:

```bash
sudo AWGPANEL_INTEGRATION=1 go test ./internal/routing -run TestNetworkNamespaceRouting -v
```

Тест создаёт только namespaces/veth с уникальным суффиксом процесса, проверяет client/global direct/WARP-наборы для IPv4/IPv6 и аварийное удаление таблицы без удаления AWG-facing интерфейса.

## Нагрузка

Измерьте idle и 10/50/100 Мбит/с отдельно для direct и WARP:

```bash
systemd-cgtop --iterations=12
systemctl show awgpanel-routing-dns awgpanel-routing-xray \
  -p MemoryCurrent -p CPUUsageNSec -p TasksCurrent
nft list chain inet awgpanel route_warp
```

Генерируйте фиксированную скорость `iperf3 -b 10M`, `50M`, `100M` через домены/IP, предварительно подтверждённые в нужных sets. Запишите CPU, RAM, packet loss и фактическую скорость. У direct-потока TProxy counters должны оставаться неизменными.
