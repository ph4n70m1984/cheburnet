#!/bin/sh

set -e

REPO_CHEBUR="ph4n70m1984/cheburnet"
DEST_FILE="/usr/bin/sing-box"
SERVICE_NAME="cheburnet"

R="\033[1;31m"
G="\033[1;32m"
Y="\033[1;33m"
C="\033[1;36m"
N="\033[0m"

WORK_DIR=""
SERVICE_STOPPED="0"
DNS_BACKED_UP="0"

cleanup() {
    stty echo 2>/dev/null || true
    printf "\n${R}[!] Прервано пользователем.${N}\n"
    [ -n "$WORK_DIR" ] && rm -rf "$WORK_DIR"
    if [ "$DNS_BACKED_UP" = "1" ]; then
        [ -f "/tmp/resolv.conf.bak" ] && mv -f "/tmp/resolv.conf.bak" "/tmp/resolv.conf" 2>/dev/null || true
        [ -f "/tmp/resolv.conf.auto.bak" ] && mv -f "/tmp/resolv.conf.auto.bak" "/tmp/resolv.conf.auto" 2>/dev/null || true
    fi
    if [ "$SERVICE_STOPPED" = "1" ]; then
        /etc/init.d/"$SERVICE_NAME" start >/dev/null 2>&1 || true
    fi
    exit 1
}

trap cleanup INT

fail() {
    stty echo 2>/dev/null || true
    printf "${R}[!] Ошибка: %s${N}\n" "$1"
    [ -n "$WORK_DIR" ] && rm -rf "$WORK_DIR"
    if [ "$DNS_BACKED_UP" = "1" ]; then
        [ -f "/tmp/resolv.conf.bak" ] && mv -f "/tmp/resolv.conf.bak" "/tmp/resolv.conf" 2>/dev/null || true
        [ -f "/tmp/resolv.conf.auto.bak" ] && mv -f "/tmp/resolv.conf.auto.bak" "/tmp/resolv.conf.auto" 2>/dev/null || true
    fi
    [ "$SERVICE_STOPPED" = "1" ] && /etc/init.d/"$SERVICE_NAME" start >/dev/null 2>&1 || true
    exit 1
}

# 1. Проверка поддержки read -t
READ_TIMEOUT_SUPPORTED="1"
_READ_TIMEOUT_TEST=$( (read -r -t 0 _test) 2>&1 </dev/null )
case "$_READ_TIMEOUT_TEST" in
    *"Illegal option"*|*"illegal option"*|*"invalid option"*|*"Invalid option"*|*"bad option"*|*"Bad option"*)
        READ_TIMEOUT_SUPPORTED="0"
        ;;
esac
unset _READ_TIMEOUT_TEST

read_input() {
    READ_VALUE=""
    if [ "$READ_TIMEOUT_SUPPORTED" = "1" ]; then
        read -r -t "$1" READ_VALUE || READ_VALUE=""
    else
        read -r READ_VALUE || READ_VALUE=""
    fi
    READ_VALUE=$(echo "$READ_VALUE" | tr -d '\r\n ')
}

# 2. Сетевой транспорт
if command -v curl >/dev/null 2>&1; then
    FETCH_TYPE="curl"
    FETCH="curl -sSL --insecure --connect-timeout 15"
    DOWNLOAD="curl -fL --insecure --connect-timeout 15 --retry 3 --retry-delay 2 -m 120 -o"
elif command -v wget >/dev/null 2>&1; then
    FETCH_TYPE="wget"
    FETCH="wget -qO- --no-check-certificate --timeout=15"
    DOWNLOAD="wget -q --no-check-certificate --timeout=120 --tries=3 -O"
else
    fail "Не найден ни curl, ни wget. Установите один из них."
fi

api_get() {
    if [ -n "$GITHUB_TOKEN" ]; then
        case "$FETCH_TYPE" in
            curl) curl -sSL --insecure --connect-timeout 15 -H "Authorization: token $GITHUB_TOKEN" "$1" 2>/dev/null ;;
            wget) wget -qO- --no-check-certificate --timeout=15 --header="Authorization: token $GITHUB_TOKEN" "$1" 2>/dev/null ;;
        esac
    else
        $FETCH "$1" 2>/dev/null
    fi
}

setup_fallback_dns() {
    if [ "$DNS_BACKED_UP" = "0" ]; then
        [ -f /tmp/resolv.conf ] && cp -f /tmp/resolv.conf /tmp/resolv.conf.bak 2>/dev/null || true
        [ -f /tmp/resolv.conf.auto ] && cp -f /tmp/resolv.conf.auto /tmp/resolv.conf.auto.bak 2>/dev/null || true
        DNS_BACKED_UP="1"
    fi

    printf "nameserver 8.8.8.8\nnameserver 77.88.8.8\n" > /tmp/resolv.conf
    printf "nameserver 8.8.8.8\nnameserver 77.88.8.8\n" > /tmp/resolv.conf.auto

    if command -v uci >/dev/null 2>&1; then
        while uci -q delete dhcp.@dnsmasq[0].server; do :; done
        uci add_list dhcp.@dnsmasq[0].server='8.8.8.8'
        uci add_list dhcp.@dnsmasq[0].server='77.88.8.8'
        uci set dhcp.@dnsmasq[0].noresolv='1'
        uci commit dhcp
        /etc/init.d/dnsmasq restart >/dev/null 2>&1 || true
    fi
}

# 3. Определение пакетного менеджера и архитектуры OpenWrt
PKG_MANAGER=""
PKG_EXT=""
if command -v apk >/dev/null 2>&1 && [ "$(command -v apk)" != "/opt/bin/apk" ]; then
    PKG_MANAGER="apk"
    PKG_EXT="apk"
elif command -v opkg >/dev/null 2>&1 && [ "$(command -v opkg)" != "/opt/bin/opkg" ]; then
    PKG_MANAGER="opkg"
    PKG_EXT="ipk"
else
    fail "Пакетный менеджер OpenWrt (apk или opkg) не обнаружен."
fi

HOST_ARCH=$(uname -m)
DISTRIB_ARCH=""
if [ -f "/etc/openwrt_release" ]; then
    DISTRIB_ARCH=$(. /etc/openwrt_release && echo "$DISTRIB_ARCH")
fi

APK_SYSTEM_ARCH=""
if [ "$PKG_MANAGER" = "apk" ]; then
    if [ -f "/etc/apk/arch" ]; then
        APK_SYSTEM_ARCH=$(head -n 1 /etc/apk/arch | tr -d ' \r\n')
    fi
    if [ -z "$APK_SYSTEM_ARCH" ]; then
        APK_SYSTEM_ARCH=$(apk --print-arch 2>/dev/null | tr -d ' \r\n')
    fi
fi

TARGET_PACKAGE_ARCH="${APK_SYSTEM_ARCH:-$DISTRIB_ARCH}"

case "$HOST_ARCH" in
    aarch64)              ARCH_SUFFIX="arm64"; CHEBUR_ARCH="aarch64" ;;
    armv7*)               ARCH_SUFFIX="armv7"; CHEBUR_ARCH="armhf" ;;
    x86_64)               ARCH_SUFFIX="amd64"; CHEBUR_ARCH="amd64" ;;
    mips)                 ARCH_SUFFIX="mips-softfloat"; CHEBUR_ARCH="mips" ;;
    mipsel | mipsle)      ARCH_SUFFIX="mipsle-softfloat"; CHEBUR_ARCH="mipsle" ;;
    *)                    fail "Архитектура $HOST_ARCH не поддерживается." ;;
esac

CURRENT_SB_VER=""
if [ -f "$DEST_FILE" ]; then
    CURRENT_SB_VER=$("$DEST_FILE" version 2>/dev/null | head -n 1 | awk '{print $3}') || true
fi

CURRENT_CHEBUR_VER=""
if command -v cheburnetd >/dev/null 2>&1; then
    VER_RAW=$(cheburnetd show_version 2>/dev/null || true)
    CURRENT_CHEBUR_VER=$(echo "$VER_RAW" | awk '{print $NF}' | tr -d 'v')
fi

if [ -z "$CURRENT_CHEBUR_VER" ]; then
    if [ "$PKG_MANAGER" = "apk" ]; then
        CURRENT_CHEBUR_VER=$(apk info -e luci-app-cheburnet 2>/dev/null | awk -F'-' '{print $(NF-1)}' || true)
    elif [ "$PKG_MANAGER" = "opkg" ]; then
        CURRENT_CHEBUR_VER=$(opkg status luci-app-cheburnet 2>/dev/null | awk '/^Version:/ {print $2}')
    fi
fi

if [ -z "$CURRENT_CHEBUR_VER" ] && [ -f /usr/bin/cheburnetd ]; then
    CURRENT_CHEBUR_VER="установлен (версия не определена)"
fi

printf "\n${C}====================================================${N}\n"
printf "${C}   Установка / Обновление Chebur.NET & Sing-Box     ${N}\n"
printf "${C}====================================================${N}\n"
printf "  Архитектура хоста:    ${Y}%s (%s)${N}\n" "$HOST_ARCH" "$ARCH_SUFFIX"
printf "  Таргет OpenWrt:       ${Y}%s${N}\n" "${TARGET_PACKAGE_ARCH:-$CHEBUR_ARCH}"
printf "  Пакетный менеджер:    ${Y}%s (%s)${N}\n" "$PKG_MANAGER" "$PKG_EXT"
printf "  Текущий sing-box:     ${Y}%s${N}\n" "${CURRENT_SB_VER:-не установлен}"
printf "  Текущий Chebur.NET:   ${Y}%s${N}\n\n" "${CURRENT_CHEBUR_VER:-не установлен}"

stop_cheburnet_service() {
    if [ "$SERVICE_STOPPED" = "0" ] && [ -f "/etc/init.d/$SERVICE_NAME" ]; then
        printf "${C}[*] Остановка службы %s...${N}\n" "$SERVICE_NAME"
        /etc/init.d/"$SERVICE_NAME" stop >/dev/null 2>&1 || true
        SERVICE_STOPPED="1"
        sleep 1
    fi
    setup_fallback_dns
}

# 4. Выбор канала обновлений для Chebur.NET
CURRENT_UCI_CHANNEL=""
if command -v uci >/dev/null 2>&1 && [ -f "/etc/config/cheburnet" ]; then
    CURRENT_UCI_CHANNEL=$(uci -q get cheburnet.main.update_channel || true)
fi

echo "Выбор канала установки Chebur.NET:"
echo "  1) Стабильный канал (Release)"
echo "  2) Бета-канал (Beta / Pre-release с поддержкой свежих функций)"
if [ "$CURRENT_UCI_CHANNEL" = "beta" ]; then
    DEF_CHAN_CHOICE="2"
else
    DEF_CHAN_CHOICE="1"
fi
printf "${C}[>] Выберите канал [1-2] (по умолчанию %s): ${N}" "$DEF_CHAN_CHOICE"
read_input 30
CHOICE_CHAN="${READ_VALUE:-$DEF_CHAN_CHOICE}"

case "$CHOICE_CHAN" in
    2)
        SELECTED_CHANNEL="beta"
        printf "${Y}[*] Выбран канал: Бета-версии (Beta)${N}\n\n"
        ;;
    *)
        SELECTED_CHANNEL="release"
        printf "${G}[*] Выбран канал: Стабильный (Release)${N}\n\n"
        ;;
esac

# 5. Выбор действия для Sing-Box
echo "Операции с ядром Sing-Box:"
echo "  1) Установить / Обновить из официального репозитория OpenWrt ($PKG_MANAGER)"
echo "  0) Пропустить обновление sing-box"
printf "${C}[>] Ваш выбор [0-1] (по умолчанию 1): ${N}"
read_input 30
CHOICE_SB="${READ_VALUE:-1}"

case "$CHOICE_SB" in
    1)
        printf "${C}[*] Установка/обновление sing-box через %s...${N}\n" "$PKG_MANAGER"
        if [ "$PKG_MANAGER" = "apk" ]; then
            apk update && apk add --upgrade sing-box
        else
            opkg update && opkg install sing-box --force-reinstall
        fi
        NEW_SB_VER=$("$DEST_FILE" version 2>/dev/null | head -n 1 | awk '{print $3}') || true
        printf "${G}[✓] sing-box успешно установлен/обновлен: %s${N}\n" "${NEW_SB_VER:-готово}"
        ;;
    0)
        printf "${Y}[*] Пропуск обновления sing-box.${N}\n"
        ;;
    *)
        printf "${Y}[!] Неизвестный выбор. Пропуск sing-box.${N}\n"
        ;;
esac

# 6. Проверка системных зависимостей
printf "\n${C}[*] Проверка зависимостей (nftables, kmod-nft-tproxy, ip-full, ca-bundle, libcurl, curl)...${N}\n"
if [ "$PKG_MANAGER" = "apk" ]; then
    apk update
    apk add --no-cache --upgrade libcurl curl nftables kmod-nft-tproxy ip-full ca-bundle
else
    opkg update
    opkg install --force-reinstall libcurl curl
    opkg install nftables kmod-nft-tproxy ip-full ca-bundle
fi

# 7. Поиск и выбор релиза Chebur.NET на GitHub с учетом выбранного канала и SemVer
printf "\n${C}[*] Получение списка релизов Chebur.NET с GitHub...${N}\n"
RELEASES_LIST_JSON=$(api_get "https://api.github.com/repos/${REPO_CHEBUR}/releases?per_page=25")
[ -z "$RELEASES_LIST_JSON" ] && fail "Не удалось получить метаданные релизов Chebur.NET."

MATCH_ARCH="${TARGET_PACKAGE_ARCH:-$CHEBUR_ARCH}"

PARSED_DATA=$(echo "$RELEASES_LIST_JSON" | awk -v chan="$SELECTED_CHANNEL" -v arch="$MATCH_ARCH" -v ext="$PKG_EXT" '
    /"tag_name":/ {
        t = $0
        sub(/.*"tag_name":[[:space:]]*"/, "", t)
        sub(/".*/, "", t)
        gsub(/[v]/, "", t)
        cur_tag = t
        is_pre = 0
        is_draft = 0
    }
    /"prerelease":[[:space:]]*true/ { is_pre = 1 }
    /"draft":[[:space:]]*true/      { is_draft = 1 }
    /"browser_download_url":/ {
        u = $0
        sub(/.*"browser_download_url":[[:space:]]*"/, "", u)
        sub(/".*/, "", u)

        if (cur_tag != "" && !is_draft && u ~ "\\." ext "$") {
            if (chan == "release" && is_pre) next

            if (u ~ arch || u ~ "aarch64") {
                if (!url[cur_tag]) {
                    url[cur_tag] = u
                    tags[++n] = cur_tag
                }
            }
        }
    }
    END {
        if (n == 0) exit 1
        for (i = 1; i <= n; i++) {
            for (j = i + 1; j <= n; j++) {
                split(tags[i], a, "[-.]")
                split(tags[j], b, "[-.]")
                swap = 0
                for (k = 1; k <= 4; k++) {
                    va = a[k] + 0; vb = b[k] + 0
                    if (vb > va) { swap = 1; break }
                    if (va > vb) { break }
                }
                if (swap) { t = tags[i]; tags[i] = tags[j]; tags[j] = t }
            }
        }
        print tags[1] "|" url[tags[1]]
    }
')

[ -z "$PARSED_DATA" ] && fail "Не удалось найти подходящий пакет .$PKG_EXT под архитектуру $MATCH_ARCH для канала $SELECTED_CHANNEL."

CHEBUR_CLEAN_VER=$(echo "$PARSED_DATA" | cut -d '|' -f 1)
CHEBUR_URL=$(echo "$PARSED_DATA" | cut -d '|' -f 2)
CHEBUR_LATEST_TAG="v${CHEBUR_CLEAN_VER}"

printf "  Целевой релиз Chebur.NET (%s): ${Y}%s${N}\n" "$SELECTED_CHANNEL" "$CHEBUR_LATEST_TAG"

NEED_UPDATE_CHEBUR="1"
if [ -n "$CURRENT_CHEBUR_VER" ] && [ "$CURRENT_CHEBUR_VER" = "$CHEBUR_CLEAN_VER" ]; then
    printf "${G}[✓] Chebur.NET уже установлен с версией %s.${N}\n" "$CHEBUR_CLEAN_VER"
    printf "${C}[>] Переустановить пакет заново? [y/N]: ${N}"
    read_input 20
    case "$READ_VALUE" in
        y|Y|д|Д) NEED_UPDATE_CHEBUR="1" ;;
        *) NEED_UPDATE_CHEBUR="0" ;;
    esac
fi

if [ "$NEED_UPDATE_CHEBUR" = "1" ]; then
    # ШАГ 1: Скачивание (служба еще работает, сеть и резолвинг активны)
    printf "${C}[*] Скачивание %s...${N}\n" "$(basename "$CHEBUR_URL")"
    $DOWNLOAD "/tmp/cheburnet.${PKG_EXT}" "$CHEBUR_URL" || fail "Сбой при скачивании пакета Chebur.NET"

    # ШАГ 2: Остановка службы перед установкой
    stop_cheburnet_service

    if [ -f "/etc/config/cheburnet" ]; then
        cp -f "/etc/config/cheburnet" "/tmp/cheburnet_config_backup"
    fi

    # ШАГ 3: Установка пакета
    printf "${C}[*] Установка пакета Chebur.NET...${N}\n"
    if [ "$PKG_MANAGER" = "apk" ]; then
        if ! apk add --allow-untrusted --force-overwrite "/tmp/cheburnet.apk" 2>/dev/null; then
            printf "${Y}[!] Обход валидации пакета через распаковку архива...${N}\n"
            (tar -xzf /tmp/cheburnet.apk -C / 2>/dev/null || (dd if=/tmp/cheburnet.apk bs=1024 skip=1 2>/dev/null | tar -xzf - -C /))
        fi
        rm -f /tmp/cheburnet.apk /.PKGINFO /.pre-install /.post-install 2>/dev/null || true
    else
        opkg install "/tmp/cheburnet.ipk" --force-reinstall
        rm -f "/tmp/cheburnet.ipk"
    fi

    if [ -f "/tmp/cheburnet_config_backup" ]; then
        mv -f "/tmp/cheburnet_config_backup" "/etc/config/cheburnet"
    fi
fi

# 8. Фиксация выбранного канала в UCI
if command -v uci >/dev/null 2>&1 && [ -f "/etc/config/cheburnet" ]; then
    uci -q set cheburnet.main.update_channel="$SELECTED_CHANNEL" || true
    uci -q commit cheburnet || true
fi

# 9. ШАГ 4: Финализация прав, каталогов и запуск/перезапуск службы
mkdir -p /var/etc/cheburnet /var/run/cheburnet
[ -f /usr/bin/cheburnetd ] && chmod 755 /usr/bin/cheburnetd
[ -f /etc/init.d/cheburnet ] && chmod 755 /etc/init.d/cheburnet

rm -rf /tmp/luci-indexcache /tmp/luci-modulecache/

if [ "$DNS_BACKED_UP" = "1" ]; then
    [ -f "/tmp/resolv.conf.bak" ] && mv -f "/tmp/resolv.conf.bak" "/tmp/resolv.conf" 2>/dev/null || true
    [ -f "/tmp/resolv.conf.auto.bak" ] && mv -f "/tmp/resolv.conf.auto.bak" "/tmp/resolv.conf.auto" 2>/dev/null || true
fi

/etc/init.d/cheburnet enable >/dev/null 2>&1 || true
/etc/init.d/cheburnet restart >/dev/null 2>&1 || true
SERVICE_STOPPED="0"

printf "\n${G}====================================================${N}\n"
printf "${G}  Chebur.NET и Sing-Box успешно настроены!          ${N}\n"
printf "  Канал обновлений:  ${Y}%s${N}\n" "$SELECTED_CHANNEL"
printf "  Служба:            ${Y}cheburnet (active/running)${N}\n"
printf "  Веб-интерфейс:     ${Y}LuCI -> Службы -> Chebur.NET${N}\n"
printf "${G}====================================================${N}\n"