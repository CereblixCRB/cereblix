# unm — установка и настройка (Windows / Linux / HiveOS)

Быстрый CPU-майнер NeuroMorph для Cereblix (CRB). Байт-идентичен консенсусу,
**в ~1.4–2.1× быстрее текущего форка xmrig-cereblix**. Масштабируется до 128
потоков (EPYC).

## Готовые файлы

| Система | Файл | Что это |
|---|---|---|
| Windows | `unm-windows-amd64.exe` | Статический .exe, **запускается сразу**, без DLL |
| Linux / EPYC / HiveOS | `unm-linux-amd64` | **Готовый статический бинарник** (musl) — копируй и запускай на любом дистрибутиве |
| Linux (для макс. скорости) | `unm-linux-src.tar.gz` | Исходники + `build.sh` — пересборка `-march=native` на самом сервере |
| HiveOS | `unm-hiveos.tar.gz` | Кастомный майнер (внутри **уже лежит готовый бинарник** — риг стартует без сборки) |

> **Готовый `unm-linux-amd64`** статически слинкован (musl) — работает на ЛЮБОМ
> Linux и на HiveOS без зависимостей. Этого достаточно для большинства.
> **Пересборка `build.sh` на самом сервере** даёт `-march=native` (тюнинг под Zen4/Zen5)
> — ещё чуть быстрее; имеет смысл на больших EPYC. VAES всё равно выбирается в рантайме.

> **Один инструмент собирает под всё — Zig.** `cross-build.sh` (в `*-src.tar.gz`)
> собирает Linux/HiveOS-бинарник одной командой **и на твоём Windows-ноуте, и на Ubuntu**
> (Zig — один скачиваемый архив, без установки и root; скрипт сам его подтянет).
> Windows-бинарник собирается через MSYS2 (`build.ps1`). HiveOS = x86-64 Linux,
> отдельной цели нет — линукс-бинарь и есть хайвос-бинарь.

## Подключение — те же адреса, что у оригинального нативного майнера

В `-node` указывайте тот же URL, что и нативному майнеру:
- **Pool:** `https://cereblix.com/pool/api`  (или `https://ru.cereblix.com/pool/api`)
- **Solo:** `https://cereblix.com/api`
- **Прямой узел:** `http://IP_УЗЛА:18751/api`

`https://` работает через **curl** (TLS/Cloudflare/редиректы — как у оригинала).
curl есть из коробки на **Windows 10/11, Linux и HiveOS**; отдельный libssl не
линкуется (поэтому нет риска libssl-крашей на HiveOS). `http://` идёт напрямую по
сокетам (без curl).

Адрес кошелька — `crb1` + 40 hex (`crb1abc…`). Кошелёк: https://cereblix.com/wallet/

---

## 1) Windows

1. Скопируйте `unm-windows-amd64.exe` в любую папку.
2. Откройте PowerShell или CMD в этой папке и запустите:

```
.\unm-windows-amd64.exe -node https://cereblix.com/pool/api -addr crb1ВАШАДРЕС -lanes auto
```

- `-threads N` — число потоков (по умолчанию все логические ядра).
- `-lanes auto` — майнер сам подберёт лучшее число лейнов под ваш CPU (~12 сек).
  Можно зафиксировать: `-lanes 1` (безопасно везде) / `-lanes 2` / `-lanes 3`.
- Проверить скорость без сети: `.\unm-windows-amd64.exe -bench 10`

Автозапуск — обычный `.bat` в автозагрузке с этой командой.

---

## 2) Linux / EPYC (сервер)

**Вариант А — готовый бинарник (быстрый старт, без сборки):**
```bash
chmod +x unm-linux-amd64
./unm-linux-amd64 -node https://cereblix.com/pool/api -addr crb1ВАШАДРЕС -lanes auto
```

**Вариант Б — пересборка на сервере (`-march=native`, чуть быстрее на больших EPYC):**
```bash
tar -xzf unm-linux-src.tar.gz && cd unm
bash build.sh                 # поставит gcc если нужно -> ./unm (-march=native)
./unm -node https://cereblix.com/pool/api -addr crb1ВАШАДРЕС -lanes auto
```

**Пересобрать под все системы одним инструментом (на ноуте или Ubuntu):**
```bash
bash cross-build.sh           # Zig -> unm-linux-amd64 (musl static, = HiveOS)
```

### Рекомендации для большого EPYC (например, 128 потоков)

```bash
# huge pages (2 MiB) под датасет + скретчпады — заметно снижает TLB-промахи.
# нужно ~ 64 MiB (датасет) + threads*lanes*2 MiB. для 128 потоков, lanes=1 ≈ 320 MiB:
sudo sysctl vm.nr_hugepages=160          # 160 * 2 MiB = 320 MiB (lanes=2 -> ставьте 320)

# раскидать общий 64 MiB датасет по всем контроллерам памяти (чтобы ни одна
# NUMA-нода не стала «горячей» по латентности), и пинить по потоку на ядро:
numactl --interleave=all \
  ./unm -node http://127.0.0.1:18751/api -addr crb1ВАШАДРЕС -lanes auto
```

- `-lanes auto` сам решит: на EPYC с большим L3 обычно выигрывает `lanes 2–3`,
  на слабом по кэшу CPU — `lanes 1`. Запустите один раз с `auto` и посмотрите вывод.
- Замер без сети: `./unm -bench 15 -threads 128 -lanes auto`

### systemd-сервис (автозапуск)

```ini
# /etc/systemd/system/unm.service
[Unit]
Description=unm CRB
After=network-online.target
[Service]
ExecStart=/usr/bin/numactl --interleave=all /opt/unm/unm \
  -node http://127.0.0.1:18751/api -addr crb1ВАШАДРЕС -lanes auto
Restart=always
[Install]
WantedBy=multi-user.target
```
```bash
sudo systemctl enable --now unm
journalctl -u unm -f      # логи + хешрейт
```

---

## 3) HiveOS

HiveOS — это Linux. Пакет `unm-hiveos.tar.gz` ставится как **кастомный майнер**.
Внутри **уже лежит готовый статический бинарник** — риг стартует сразу, без сборки и
без gcc. (Если бинарник почему-то не запустится на CPU рига, `h-run.sh` сам соберёт
его из вложенных исходников — нужен `gcc`, обычно есть.)

### Установка

1. **Загрузите пакет в HiveOS.** Два способа:
   - Положите `unm-hiveos.tar.gz` на доступный по HTTP адрес и в HiveOS:
     *Flight Sheets → Miners → Add Miner → Custom →* в поле *Installation URL*
     укажите ссылку на тарбол.
   - Либо вручную на риге:
     ```bash
     cd /hive/miners/custom
     tar -xzf /path/unm-hiveos.tar.gz     # появится папка unm/
     ```

2. **Создайте Flight Sheet** (Coin: любой/Custom, Miner: `unm`):
   - **Wallet and worker template:** ваш адрес `crb1…` (можно с `.worker`).
   - **Pool URL:** `https://cereblix.com/pool/api` (как у нативного майнера);
     solo — `https://cereblix.com/api`; прямой узел — `http://IP:18751/api`.
   - **Extra config arguments** (необязательно):
     `-threads 6 -lanes auto`  (по умолчанию: все ядра + `-lanes auto`).

3. Примените Flight Sheet к ригу. При первом старте майнер соберётся
   (`building unm for this rig...`), затем начнёт майнить. Хешрейт и принятые
   шары появятся на дашборде HiveOS.

> Хотите без сборки на риге? Соберите бинарник один раз на Linux
> (`bash build.sh portable`), положите получившийся `unm` рядом со скриптами
> в `hiveos/unm/`, пересоберите тарбол `bash package-hiveos.sh` — тогда
> `h-run.sh` использует готовый бинарник и пропустит сборку.

### Файлы пакета HiveOS
`h-manifest.conf` (мета), `h-config.sh` (флайт-шит → аргументы),
`h-run.sh` (сборка-при-первом-старте + запуск), `stats.sh` (хешрейт/шары на дашборд).

---

## Многосокетный EPYC — турнкей NUMA-запуск (Linux)

На боксе с >1 NUMA-нодой (2 сокета и т.п.) общий 64 MiB датасет — «горячий» по
латентности, и половина потоков ходит в чужую память. Скрипт `run-numa.sh` сам
определяет ноды и запускает по одному процессу на ноду с локальной памятью:

```bash
./run-numa.sh -node https://cereblix.com/pool/api -addr crb1ВАШАДРЕС -lanes auto
```
Один процесс на сокет, каждый со своим датасетом в локальной DRAM → выше суммарный
H/s. Без root (нужен `numactl`; если его нет — запустит один процесс). На односокетном
боксе просто запускает один процесс.

## Авто-подстройка под процессор

Майнер сам подбирает оптимум под железо при старте:
- **fill-путь** (заполнение скретча): меряет AES-NI / VAES-256 / **VAES-512** и
  берёт самый быстрый КОРРЕКТНЫЙ (Zen3+ → VAES-256, Zen4/Zen5 Genoa/Turin → часто
  VAES-512, Zen2/7B12 → AES-NI). В логе: `fill: VAES-256`.
- **lanes**: `-lanes auto` перебирает {1,2,3,4,6,8} под твой кэш.

Ручное управление (env): `NM_NO_VAES=1` — форс AES-NI; `NM_FILL=aesni|vaes256|vaes512`
— форс конкретного пути (для A/B). Пример замера выигрыша VAES на твоём чипе:
```bash
NM_NO_VAES=1 ./unm -bench 12 -threads 252 -lanes 2   # AES-NI
            ./unm -bench 12 -threads 252 -lanes 2     # авто (VAES)
```

## Проверка и тюнинг (любая ОС)

```
unm -bench 10                  # агрегатный H/s на всех ядрах, без сети
unm -bench 10 -lanes auto      # с автоподбором лейнов
unm ... -lanes 1|2|3|6|8       # ручная фиксация лейнов
unm ... -threads N             # ограничить число потоков
```

Подробности про архитектуру, замеры и роадмап — в `README.md`.
Одно ядро — один голос.
