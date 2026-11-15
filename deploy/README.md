# Развёртывание на сервер

Схема: бинарник cue-vision под systemd от пользователя `cue-vision`, PostgreSQL на той же
машине (вход по Unix-сокету, peer), nginx — HTTPS, статика фронта тренировок и прокси `/api`
в сервис на `127.0.0.1:8080`. Проверено под AlmaLinux 10, PostgreSQL 18 (PGDG), nginx +
certbot. Docker не нужен.

| Файл | Куда на сервере |
|---|---|
| `cue-vision.service` | `/etc/systemd/system/cue-vision.service` |
| `env.example` | `/etc/cue-vision/env` (root:cue-vision, 640) |
| `nginx/cue-vision.conf` | `/etc/nginx/conf.d/cue-vision.conf` |
| `nginx/snippets/cue-vision-host.conf.example` | `/etc/nginx/snippets/cue-vision-host.conf` — с вашим доменом, не в git |
| `cue-vision-backup.cron` | `/etc/cron.d/cue-vision-backup` |
| `deploy.sh` | запускается на своей машине: сборка, выкладка, откат |
| `remote.sh` | серверная часть `deploy.sh`, загружается им сам |

## Первичная настройка

Все команды — на сервере, кроме отмеченных, из каталога с файлами `deploy/`
(с своей машины: `scp -r deploy user@server:cue-vision-deploy`). `cv.example.com` — ваш
домен, его A-запись уже смотрит на сервер.

**1. Пользователь и каталоги.** Пользователь `cue-vision` — системный, без входа и без
права записи куда-либо:

```
sudo useradd --system --no-create-home --shell /sbin/nologin cue-vision   # если ещё не заведён
sudo install -d -m 755 /opt/cue-vision/bin /srv/cue-vision
sudo install -d -m 750 -g cue-vision /etc/cue-vision
sudo semanage fcontext -a -t httpd_sys_content_t "/srv/cue-vision(/.*)?"
sudo restorecon -Rv /srv/cue-vision
```

**2. База.** Роль называется как пользователь ОС — тогда peer пускает без пароля:

```
sudo -u postgres psql <<'SQL'
CREATE ROLE "cue-vision" LOGIN;
CREATE DATABASE cuevision OWNER "cue-vision";
REVOKE CONNECT ON DATABASE cuevision FROM PUBLIC;
SQL
sudo -u cue-vision psql -h /run/postgresql -d cuevision -c 'select current_user;'
```

Миграции сервис применяет сам при старте.

**3. Настройки.** Скопировать `env.example` в `/etc/cue-vision/env`, заполнить `API_TOKEN`
(`openssl rand -hex 32`) и `ADMIN_PASSWORD`:

```
sudo install -m 640 -o root -g cue-vision env.example /etc/cue-vision/env
sudoedit /etc/cue-vision/env
```

Порт 8080 должен быть свободен: `sudo ss -ltnp | grep 8080`. Если занят — сменить и в
`HTTP_ADDR`, и в `nginx/cue-vision.conf`.

**4. Юнит.**

```
sudo install -m 644 cue-vision.service /etc/systemd/system/
sudo systemctl daemon-reload
sudo systemctl enable cue-vision      # запустит его deploy.sh
```

**5. Первая выкладка** — на своей машине, из каталога `cue-vision`:

```
deploy/deploy.sh user@server
```

В журнале (`journalctl -u cue-vision -n 50`) — применённые миграции и заведённый
администратор. После этого стереть из `/etc/cue-vision/env` обе строки, `ADMIN_LOGIN` и
`ADMIN_PASSWORD`: они нужны, только пока пользователей нет. Задаются они только вместе —
оставленный без пароля `ADMIN_LOGIN` не даст сервису запуститься.

**6. Сертификат и nginx.** Сначала сертификат: конфиг ссылается на его файлы, без них
`nginx -t` не пройдёт.

```
sudo certbot certonly --nginx -d cv.example.com
sudo mkdir -p /etc/nginx/snippets
sudo cp nginx/snippets/cue-vision-host.conf.example /etc/nginx/snippets/cue-vision-host.conf
sudoedit /etc/nginx/snippets/cue-vision-host.conf      # подставить домен
sudo install -m 644 nginx/cue-vision.conf /etc/nginx/conf.d/
getsebool httpd_can_network_connect                    # если off: sudo setsebool -P httpd_can_network_connect 1
sudo nginx -t && sudo systemctl reload nginx
```

Порты 80 и 443 в firewalld должны быть открыты (`sudo firewall-cmd --list-all`); 8080 и
5432 не открывать.

**7. Резервные копии.**

```
sudo install -d -m 700 -o postgres -g postgres /var/backups/cue-vision
sudo install -m 644 cue-vision-backup.cron /etc/cron.d/cue-vision-backup
```

Дампы лежат на том же диске — время от времени их стоит забирать к себе. Восстановление:
`sudo -u postgres pg_restore -d cuevision --clean <файл>`.

## Проверка

1. `https://cv.example.com` — вход администратором. Работает — значит cookie (`COOKIE_SECURE`)
   и проверка `Origin` за прокси в порядке.
2. Завести пользователя `staff` для тренера.
3. Загрузка событий токеном (своя машина):

   ```
   curl -H "Authorization: Bearer $API_TOKEN" -X POST \
     'https://cv.example.com/api/v1/events?stream=test' --data-binary @testdata/session_100s.jsonl
   ```

4. Снаружи `http://<IP>:8080` недоступен.

## Обновление

На своей машине: `deploy/deploy.sh user@server`. Скрипт собирает сервис и фронт из
рабочего дерева (о незакоммиченных изменениях предупреждает), на сервере снимает дамп
базы, ставит новую версию, перезапускает сервис и ждёт `/readyz`; если сервис не поднялся —
печатает журнал. nginx перезапускать не нужно. Серверная часть — `remote.sh`, её загружает
`deploy.sh`.

- Дамп перед каждой выкладкой: `/var/backups/cue-vision/pre-deploy-*.dump`, хранятся 5
  последних. Миграции новой версии применяются при её старте, то есть уже после дампа.
- Прежняя версия остаётся рядом: `/opt/cue-vision/bin/cue-vision.prev`,
  `/srv/cue-vision/training.prev`.

## Откат

```
deploy/deploy.sh user@server --rollback
```

Меняет местами текущую и предыдущую выкладки (повторный откат вернёт новую) и
перезапускает сервис. Базу откат не трогает: старый бинарник пропускает неизвестные ему
миграции, поэтому после миграции, которая только добавляет таблицы и столбцы, он работает и
так. Вернуть базу к состоянию до выкладки нужно, только если миграция удаляла или меняла
данные; всё записанное после выкладки при этом пропадёт. Команды восстановления скрипт
печатает сам:

```
sudo systemctl stop cue-vision
sudo -u postgres dropdb cuevision
sudo -u postgres pg_restore --create -d postgres /var/backups/cue-vision/pre-deploy-<время>.dump
sudo systemctl start cue-vision
```

`--create` пересоздаёт базу с прежним владельцем и правами.

## Известное ограничение

Сервис берёт адрес клиента из соединения и не верит `X-Forwarded-For`. За nginx все
приходят с `127.0.0.1`, поэтому блокировка после 10 неудачных входов действует на логин
целиком, а не на адрес: подбор пароля к `admin` с любого адреса закроет вход всем на
15 минут.
