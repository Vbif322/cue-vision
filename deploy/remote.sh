#!/bin/sh
# Серверная половина deploy.sh. Её загружает и запускает deploy.sh (от root через sudo),
# руками запускать не нужно.
#
#   remote.sh deploy   <stage>   дамп базы, поставить бинарник и фронт из <stage>
#   remote.sh rollback <stage>   поменять местами текущую и предыдущую (.prev) выкладки
#
# <stage> — временный каталог с загруженными файлами, удаляется в конце.
set -eu

mode=$1
stage=$2
bin=/opt/cue-vision/bin/cue-vision
web=/srv/cue-vision/training
backups=/var/backups/cue-vision
keep_dumps=5

trap 'rm -rf "$stage"' EXIT
# pg_dump от postgres не может войти в каталог stage, принадлежащий пользователю.
cd /

wait_ready() {
    for _ in 1 2 3 4 5 6 7 8 9 10; do
        if curl -fsS http://127.0.0.1:8080/readyz >/dev/null 2>&1; then
            echo "readyz ok"
            return 0
        fi
        sleep 1
    done
    echo "сервис не поднялся за 10 с:"
    systemctl status cue-vision --no-pager -l || true
    journalctl -u cue-vision -n 30 --no-pager -o cat
    return 1
}

latest_dump() {
    ls -1t "$backups"/pre-deploy-*.dump 2>/dev/null | head -n 1
}

# Дамп до перезапуска: миграции новой версии применятся при её старте.
dump() {
    install -d -m 700 -o postgres -g postgres "$backups"
    file=$backups/pre-deploy-$(date +%Y%m%d-%H%M%S).dump
    runuser -u postgres -- pg_dump -Fc -f "$file" cuevision
    echo "дамп базы: $file"
    ls -1t "$backups"/pre-deploy-*.dump | tail -n +$((keep_dumps + 1)) | xargs -r rm -f
}

deploy() {
    dump

    # install, а не mv: файл получает контекст SELinux каталога назначения, а не /tmp.
    install -m 755 -o root -g root "$stage/cue-vision" "$bin.new"
    if [ -e "$bin" ]; then cp -p "$bin" "$bin.prev"; fi
    mv -f "$bin.new" "$bin"

    # Фронт подменяется переименованием каталога, чтобы не отдавать половину сборки.
    rm -rf "$web.new"
    cp -r "$stage/training" "$web.new"
    chown -R root:root "$web.new"
    chmod -R u=rwX,go=rX "$web.new"
    if [ -d "$web" ]; then
        rm -rf "$web.prev"
        mv "$web" "$web.prev"
    fi
    mv "$web.new" "$web"

    restorecon -R /opt/cue-vision /srv/cue-vision
    systemctl restart cue-vision
    wait_ready
}

swap() {
    mv "$1" "$1.next"
    mv "$1.prev" "$1"
    mv "$1.next" "$1.prev"
}

# Откат меняет местами текущую и предыдущую выкладки: повторный откат вернёт новую.
# База не откатывается. Старый бинарник неизвестные ему миграции пропускает, так что
# после добавляющей миграции он работает и так; восстанавливать базу нужно, только если
# миграция удаляла или меняла данные.
rollback() {
    if [ ! -e "$bin.prev" ] || [ ! -d "$web.prev" ]; then
        echo "предыдущей выкладки нет: $bin.prev или $web.prev отсутствует"
        exit 1
    fi
    swap "$bin"
    swap "$web"
    systemctl restart cue-vision
    ok=0
    wait_ready || ok=1

    dump_file=$(latest_dump)
    echo
    echo "Код откачен, база — нет. Если нужно вернуть и базу к состоянию до последней"
    echo "выкладки (всё записанное после неё пропадёт):"
    echo "  sudo systemctl stop cue-vision"
    echo "  sudo -u postgres dropdb cuevision"
    echo "  sudo -u postgres pg_restore --create -d postgres ${dump_file:-<дамп>}"
    echo "  sudo systemctl start cue-vision"
    return $ok
}

case $mode in
    deploy) deploy ;;
    rollback) rollback ;;
    *) echo "неизвестный режим: $mode" >&2; exit 2 ;;
esac
