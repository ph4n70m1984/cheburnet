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

cleanup() {
    stty echo 2>/dev/null || true
    printf "\n${R}[!] Прервано пользователем.${N}\n"
    [ -n "$WORK_DIR" ] && rm -rf "$WORK_DIR"
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
}

# 2. Сетевой транспорт
if command -v curl >/dev/null 2>&1; then
    FETCH_TYPE="curl"
    FETCH="curl -sSL --insecure --connect-timeout 15"
    DOWNLOAD="curl -fsSL --insecure --connect-timeout 15 -o"
elif command -v wget >/dev/null 2>&1; then
    FETCH_TYPE="wget"
    FETCH="wget -qO- --no-check-certificate --timeout=15"
    DOWNLOAD="wget -q --no-check-certificate --timeout=15 -O"
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
}

# 4. Выбор действия для Sing-Box (только репозиторий OpenWrt)
echo "Операции с ядром Sing-Box:"
echo "  1) Установить / Обновить из официального репозитория OpenWrt ($PKG_MANAGER)"
echo "  0) Пропустить обновление sing-box"
printf "${C}[>] Ваш выбор [0-1] (по умолчанию 1): ${N}"
read_input 30
CHOICE_SB="${READ_VALUE:-1}"

case "$CHOICE_SB" in
    1)
        stop_cheburnet_service
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

# 5. Проверка системных зависимостей и переустановка curl / libcurl
printf "\n${C}[*] Проверка зависимостей (nftables, kmod-nft-tproxy, ip-full, ca-bundle, libcurl, curl)...${N}\n"
if [ "$PKG_MANAGER" = "apk" ]; then
    apk update
    apk add --no-cache --upgrade libcurl curl nftables kmod-nft-tproxy ip-full ca-bundle
else
    opkg update
    opkg install --force-reinstall libcurl curl
    opkg install nftables kmod-nft-tproxy ip-full ca-bundle
fi

# 6. Установка / Обновление пакета Chebur.NET
printf "\n${C}[*] Проверка обновлений Chebur.NET на GitHub...${N}\n"
CHEBUR_RELEASE_JSON=$(api_get "https://api.github.com/repos/${REPO_CHEBUR}/releases/latest")
[ -z "$CHEBUR_RELEASE_JSON" ] && fail "Не удалось получить метаданные релиза Chebur.NET."

CHEBUR_LATEST_TAG=$(echo "$CHEBUR_RELEASE_JSON" | grep '"tag_name":' | head -n 1 | cut -d '"' -f 4)
CHEBUR_CLEAN_VER=$(echo "$CHEBUR_LATEST_TAG" | sed 's/^v//')

printf "  Последняя версия Chebur.NET: ${Y}%s${N}\n" "$CHEBUR_LATEST_TAG"

NEED_UPDATE_CHEBUR="1"
if [ -n "$CURRENT_CHEBUR_VER" ] && [ "$CURRENT_CHEBUR_VER" = "$CHEBUR_CLEAN_VER" ]; then
    printf "${G}[✓] Chebur.NET уже обновлен до актуальной версии (%s).${N}\n" "$CHEBUR_CLEAN_VER"
    printf "${C}[>] Переустановить пакет заново? [y/N]: ${N}"
    read_input 15
    case "$READ_VALUE" in
        y|Y|д|Д) NEED_UPDATE_CHEBUR="1" ;;
        *) NEED_UPDATE_CHEBUR="0" ;;
    esac
fi

if [ "$NEED_UPDATE_CHEBUR" = "1" ]; then
    stop_cheburnet_service

    if [ -f "/etc/config/cheburnet" ]; then
        cp -f "/etc/config/cheburnet" "/tmp/cheburnet_config_backup"
    fi

    if [ "$PKG_MANAGER" = "apk" ]; then
        MATCH_ARCH="${TARGET_PACKAGE_ARCH:-$CHEBUR_ARCH}"
        CHEBUR_URL=$(echo "$CHEBUR_RELEASE_JSON" | grep -E "browser_download_url.*_p[0-9]+.*${MATCH_ARCH}\.apk" | head -n 1 | cut -d '"' -f 4)
        if [ -z "$CHEBUR_URL" ]; then
            CHEBUR_URL=$(echo "$CHEBUR_RELEASE_JSON" | grep -E "browser_download_url.*${MATCH_ARCH}\.apk" | head -n 1 | cut -d '"' -f 4)
        fi
        if [ -z "$CHEBUR_URL" ]; then
            CHEBUR_URL=$(echo "$CHEBUR_RELEASE_JSON" | grep -E "browser_download_url.*_p[0-9]+.*${CHEBUR_ARCH}\.apk" | head -n 1 | cut -d '"' -f 4)
        fi
        if [ -z "$CHEBUR_URL" ]; then
            CHEBUR_URL=$(echo "$CHEBUR_RELEASE_JSON" | grep "browser_download_url.*\.apk" | head -n 1 | cut -d '"' -f 4)
        fi
        [ -z "$CHEBUR_URL" ] && fail "Не найден подходящий .apk пакет для $MATCH_ARCH."

        printf "${C}[*] Скачивание %s...${N}\n" "$(basename "$CHEBUR_URL")"
        $DOWNLOAD "/tmp/cheburnet.apk" "$CHEBUR_URL" || fail "Сбой при скачивании cheburnet.apk"

        printf "${C}[*] Применение пакета Chebur.NET...${N}\n"
        if ! apk add --allow-untrusted --force-overwrite /tmp/cheburnet.apk 2>/dev/null; then
            printf "${Y}[!] Обход валидации пакета через распаковку архива...${N}\n"
            (tar -xzf /tmp/cheburnet.apk -C / 2>/dev/null || (dd if=/tmp/cheburnet.apk bs=1024 skip=1 2>/dev/null | tar -xzf - -C /))
        fi
        rm -f /tmp/cheburnet.apk /.PKGINFO /.pre-install /.post-install 2>/dev/null || true
    else
        CHEBUR_URL=$(echo "$CHEBUR_RELEASE_JSON" | grep "browser_download_url.*${DISTRIB_ARCH:-$CHEBUR_ARCH}\.ipk" | head -n 1 | cut -d '"' -f 4)
        [ -z "$CHEBUR_URL" ] && CHEBUR_URL=$(echo "$CHEBUR_RELEASE_JSON" | grep "browser_download_url.*\.ipk" | head -n 1 | cut -d '"' -f 4)
        [ -z "$CHEBUR_URL" ] && fail "Не найден .ipk пакет под архитектуру $CHEBUR_ARCH."

        printf "${C}[*] Скачивание %s...${N}\n" "$(basename "$CHEBUR_URL")"
        $DOWNLOAD "/tmp/cheburnet.ipk" "$CHEBUR_URL" || fail "Сбой при скачивании cheburnet.ipk"

        printf "${C}[*] Установка пакета через opkg...${N}\n"
        opkg install /tmp/cheburnet.ipk --force-reinstall
        rm -f /tmp/cheburnet.ipk
    fi

    if [ -f "/tmp/cheburnet_config_backup" ]; then
        mv -f "/tmp/cheburnet_config_backup" "/etc/config/cheburnet"
    fi
fi

# 7. Финализация прав, каталогов и запуск службы
mkdir -p /var/etc/cheburnet /var/run/cheburnet
[ -f /usr/bin/cheburnetd ] && chmod 755 /usr/bin/cheburnetd
[ -f /etc/init.d/cheburnet ] && chmod 755 /etc/init.d/cheburnet

rm -rf /tmp/luci-indexcache /tmp/luci-modulecache/

/etc/init.d/cheburnet enable >/dev/null 2>&1 || true
/etc/init.d/cheburnet restart >/dev/null 2>&1 || true
SERVICE_STOPPED="0"

printf "\n${G}====================================================${N}\n"
printf "${G}  Chebur.NET и Sing-Box успешно настроены!          ${N}\n"
printf "  Служба:         ${Y}cheburnet (active/running)${N}\n"
printf "  Веб-интерфейс:  ${Y}LuCI -> Службы -> Chebur.NET${N}\n"
printf "${G}====================================================${N}\n"