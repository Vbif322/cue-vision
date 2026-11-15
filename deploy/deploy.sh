#!/usr/bin/env bash
# Сборка на своей машине и выкладка на сервер: бинарник и фронт тренировок.
# Первичная настройка сервера (юнит, env, nginx, база) — deploy/README.md.
#
#   deploy/deploy.sh user@server              собрать и выложить рабочее дерево
#   deploy/deploy.sh user@server --rollback   вернуть предыдущую выкладку
#
# Перед выкладкой снимается дамп базы (/var/backups/cue-vision/pre-deploy-*.dump, хранятся
# 5 последних), прежние бинарник и фронт остаются рядом с суффиксом .prev. Откат меняет
# текущую и предыдущую выкладки местами; базу он не трогает, а команду её восстановления
# из дампа печатает. Серверная часть — deploy/remote.sh.
#
# GOARCH=arm64 — для ARM-сервера. На сервере нужен sudo; rsync не нужен.
set -euo pipefail

usage="usage: deploy/deploy.sh user@server [--rollback]"
host=${1:?$usage}
case ${2:-} in
    "") mode=deploy ;;
    --rollback) mode=rollback ;;
    *) echo "$usage" >&2; exit 2 ;;
esac
cd "$(dirname "$0")/.."

if [ "$mode" = deploy ]; then
    if [ -n "$(git status --porcelain)" ]; then
        echo "!! в рабочем дереве незакоммиченные изменения — они попадут в выкладку"
    fi

    echo "== сборка сервиса"
    GOOS=linux GOARCH="${GOARCH:-amd64}" CGO_ENABLED=0 \
        go build -trimpath -ldflags="-s -w" -o bin/cue-vision-linux ./cmd/cue-vision

    echo "== сборка фронта"
    [ -d web/node_modules ] || (cd web && npm ci)
    (cd web && npm run build --workspace @cue-vision/training)
fi

echo "== загрузка на $host"
stage=$(ssh "$host" mktemp -d)
scp -q deploy/remote.sh "$host:$stage/remote.sh"
if [ "$mode" = deploy ]; then
    scp -q bin/cue-vision-linux "$host:$stage/cue-vision"
    tar -C web/apps/training/dist -czf - . | ssh "$host" "mkdir $stage/training && tar -C $stage/training -xzf -"
fi

echo "== $([ "$mode" = deploy ] && echo установка || echo откат)"
ssh -t "$host" "sudo sh $stage/remote.sh $mode $stage"
echo "== готово"
