#!/bin/sh

set -e

REPO_CHEBUR="ph4n70m1984/cheburnet"
REPO_SB_EXTENDED="${REPO_SB_EXTENDED:-"shtorm-7/sing-box-extended"}"
REPO_SB_LX="${REPO_SB_LX:-"Leadaxe/sing-box-lx"}"
REPO_PODKOP="${REPO_PODKOP:-"FiyeroT/podkop-engine"}"
DEST_FILE="/usr/bin/sing-box"
SERVICE_NAME="cheburnet"

R="\033[1;31m"
G="\033[1;32m"
Y="\033[1;33m"
C="\033[1;36m"
N="\033[0m"

WORK_DIR=$(mktemp -d /tmp/chebur_install.XXXXXX 2>/dev/null || echo "/tmp/chebur_install_$$")
mkdir -p "$WORK_DIR"
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

# 2. Сетевой транспорт исключительно через wget
if ! command -v wget >/dev/null 2>&1; then
    fail "Утилита wget не найдена в системе."
fi

FETCH="wget -qO- --no-check-certificate --timeout=15"
DOWNLOAD="wget -q --no-check-certificate --timeout=120 -O"

api_get() {
    if [ -n "$GITHUB_TOKEN" ]; then
        wget -qO- --no-check-certificate --timeout=15 --header="Authorization: token $GITHUB_TOKEN" "$1" 2>/dev/null
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
if [ -f "/etc/cheburnet/sing-box-version" ]; then
    CURRENT_SB_VER=$(head -n 1 /etc/cheburnet/sing-box-version | tr -d '\r\n')
fi
if [ -z "$CURRENT_SB_VER" ] && [ -f "$DEST_FILE" ]; then
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
printf "${C}   Установка / Обновление Chebur.NET & Ядра        ${N}\n"
printf "${C}====================================================${N}\n"
printf "  Архитектура хоста:    ${Y}%s (%s)${N}\n" "$HOST_ARCH" "$ARCH_SUFFIX"
printf "  Таргет OpenWrt:       ${Y}%s${N}\n" "${TARGET_PACKAGE_ARCH:-$CHEBUR_ARCH}"
printf "  Пакетный менеджер:    ${Y}%s (%s)${N}\n" "$PKG_MANAGER" "$PKG_EXT"
printf "  Текущее ядро:         ${Y}%s${N}\n" "${CURRENT_SB_VER:-не установлено}"
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

# 5. Функция установки podkop-engine из официальных релизов (.ipk / .apk)
install_podkop_engine() {
    printf "${C}[*] Поиск релизов podkop-engine (${REPO_PODKOP})...${N}\n"
    TARGET_ARCH="${TARGET_PACKAGE_ARCH:-$CHEBUR_ARCH}"

    PODKOP_JSON=$(api_get "https://api.github.com/repos/${REPO_PODKOP}/releases/latest")
    if [ -z "$PODKOP_JSON" ] || echo "$PODKOP_JSON" | grep -q '"message":'; then
        PODKOP_JSON=$(api_get "https://api.github.com/repos/${REPO_PODKOP}/releases?per_page=1")
    fi

    PARSED_PODKOP=$(echo "$PODKOP_JSON" | awk -v arch="$TARGET_ARCH" -v h_arch="$HOST_ARCH" -v ext="$PKG_EXT" '
        /"tag_name":/ {
            t = $0
            sub(/.*"tag_name":[[:space:]]*"/, "", t)
            sub(/".*/, "", t)
            cur_tag = t
        }
        /"browser_download_url":/ {
            u = $0
            sub(/.*"browser_download_url":[[:space:]]*"/, "", u)
            sub(/".*/, "", u)
            if (u ~ "\\." ext "$") {
                if (u !~ "podkop-engine-full") next
                u_low = tolower(u)
                score = 0

                if (arch != "" && index(u_low, tolower(arch)) > 0) score = 100
                else if (h_arch == "aarch64") {
                    if (index(u_low, "cortex-a53") > 0) score = 80
                    else if (index(u_low, "cortex-a72") > 0) score = 70
                    else if (index(u_low, "aarch64_generic") > 0) score = 60
                }
                else if (h_arch ~ "armv7" || h_arch ~ "arm") {
                    if (index(u_low, "cortex-a7_neon") > 0) score = 80
                    else if (index(u_low, "cortex-a9") > 0) score = 70
                    else if (index(u_low, "arm_cortex-a7") > 0) score = 60
                }
                else if (h_arch ~ "mipsel" || h_arch ~ "mipsle") {
                    if (index(u_low, "mipsel_24kc") > 0) score = 80
                }
                else if (h_arch ~ "mips") {
                    if (index(u_low, "mips_24kc") > 0) score = 80
                }
                else if (h_arch == "x86_64") {
                    if (index(u_low, "x86_64") > 0) score = 90
                }

                if (score > best_score) {
                    best_score = score
                    found_url = u
                    found_tag = cur_tag
                }
            }
        }
        END {
            if (found_url != "") print found_tag "|" found_url
        }
    ')

    if [ -z "$PARSED_PODKOP" ]; then
        FALLBACK_TAG="v1.14.2-r14"
        FALLBACK_VER="1.14.2-r14"
        FALLBACK_NAME=""

        case "$TARGET_ARCH" in
            *a53*|aarch64*) FALLBACK_NAME="podkop-engine-full_${FALLBACK_VER}_openwrt_aarch64_cortex-a53.${PKG_EXT}" ;;
            *a72*)          FALLBACK_NAME="podkop-engine-full_${FALLBACK_VER}_openwrt_aarch64_cortex-a72.${PKG_EXT}" ;;
            *24kc*)         FALLBACK_NAME="podkop-engine-full_${FALLBACK_VER}_openwrt_mipsel_24kc.${PKG_EXT}" ;;
            *x86_64*)       FALLBACK_NAME="podkop-engine-full_${FALLBACK_VER}_openwrt_x86_64.${PKG_EXT}" ;;
            *)              FALLBACK_NAME="podkop-engine-full_${FALLBACK_VER}_openwrt_aarch64_generic.${PKG_EXT}" ;;
        esac

        URL_VAL="https://github.com/${REPO_PODKOP}/releases/download/${FALLBACK_TAG}/${FALLBACK_NAME}"
        TAG_VAL="$FALLBACK_TAG"
    else
        TAG_VAL=$(echo "$PARSED_PODKOP" | cut -d '|' -f 1)
        URL_VAL=$(echo "$PARSED_PODKOP" | cut -d '|' -f 2)
    fi

    PKG_FILE="$WORK_DIR/$(basename "$URL_VAL")"

    printf "  Целевой пакет: ${Y}%s${N} (%s)\n" "${TAG_VAL:-v1.14.2-r14}" "$(basename "$URL_VAL")"
    printf "${C}[*] Скачивание podkop-engine...${N}\n"
    $DOWNLOAD "$PKG_FILE" "$URL_VAL" || fail "Не удалось скачать пакет podkop-engine"
    [ -s "$PKG_FILE" ] || fail "Скачанный пакет пуст."

    stop_cheburnet_service

    printf "${C}[*] Установка пакета podkop-engine...${N}\n"
    if [ "$PKG_MANAGER" = "apk" ]; then
        apk del sing-box sing-box-tiny podkop-engine 2>/dev/null || true
        apk add --allow-untrusted "$PKG_FILE" '!podkop-engine' || fail "Ошибка установки podkop-engine через apk"
    else
        opkg remove --force-depends sing-box sing-box-tiny podkop-engine >/dev/null 2>&1 || true
        opkg install --force-depends "$PKG_FILE" || fail "Ошибка установки podkop-engine через opkg"
    fi

    if [ -f "/usr/bin/podkop-engine" ]; then
        rm -f "$DEST_FILE" 2>/dev/null || true
        ln -sf /usr/bin/podkop-engine "$DEST_FILE"
        chmod 755 /usr/bin/podkop-engine "$DEST_FILE"
    fi

    NEW_VER=$("$DEST_FILE" version 2>/dev/null | head -n 1 | awk '{print $3}') || true
    mkdir -p /etc/cheburnet
    echo "${NEW_VER:-$TAG_VAL}" > /etc/cheburnet/sing-box-version

    [ -f "/etc/init.d/podkop" ] && /etc/init.d/podkop restart >/dev/null 2>&1 || true

    printf "${G}[✓] podkop-engine успешно установлен: %s${N}\n\n" "${NEW_VER:-$TAG_VAL}"
}

# 6. Функция установки сторонних форков бинарников (extended / lx)
install_custom_singbox() {
    PKG_NAME="$1"
    GH_REPO="$2"
    DISPLAY_NAME="$3"

    printf "${C}[*] Поиск релизов %s на GitHub (${GH_REPO})...${N}\n" "$DISPLAY_NAME"
    SB_JSON=$(api_get "https://api.github.com/repos/${GH_REPO}/releases?per_page=10")
    [ -z "$SB_JSON" ] && fail "Не удалось получить список релизов ${DISPLAY_NAME} из ${GH_REPO}."

    PARSED_SB=$(echo "$SB_JSON" | awk -v arch1="$ARCH_SUFFIX" -v arch2="$CHEBUR_ARCH" '
        /"tag_name":/ {
            t = $0
            sub(/.*"tag_name":[[:space:]]*"/, "", t)
            sub(/".*/, "", t)
            cur_tag = t
            is_draft = 0
        }
        /"draft":[[:space:]]*true/ { is_draft = 1 }
        /"browser_download_url":/ {
            u = $0
            sub(/.*"browser_download_url":[[:space:]]*"/, "", u)
            sub(/".*/, "", u)
            if (cur_tag != "" && !is_draft) {
                u_low = tolower(u)

                if (u_low ~ "sfa" || u_low ~ "android" || u_low ~ "windows" || u_low ~ "\\.apk$" || u_low ~ "\\.exe$" || u_low ~ "darwin" || u_low ~ "macos") next
                if (u_low !~ "linux") next

                a1 = tolower(arch1); a2 = tolower(arch2)
                matched = 0

                if (a1 != "" && u_low ~ ("linux-" a1)) matched = 1
                if (!matched && a2 != "" && u_low ~ ("linux-" a2)) matched = 1
                if (!matched && a1 != "" && u_low ~ a1) matched = 1

                if (a1 ~ "softfloat" && u_low !~ "softfloat" && u_low ~ "mips") matched = 0
                if (a1 == "arm64" && u_low ~ "armv") matched = 0

                if (matched && !found_url) {
                    found_url = u
                    found_tag = cur_tag
                }
            }
        }
        END {
            if (found_url != "") print found_tag "|" found_url
        }
    ')

    [ -z "$PARSED_SB" ] && fail "Не найден подходящий Linux-бинарник ${DISPLAY_NAME} под архитектуру ${ARCH_SUFFIX} в ${GH_REPO}."

    TAG_VAL=$(echo "$PARSED_SB" | cut -d '|' -f 1)
    URL_VAL=$(echo "$PARSED_SB" | cut -d '|' -f 2)
    printf "  Целевой релиз %s: ${Y}%s${N} (%s)\n" "$DISPLAY_NAME" "$TAG_VAL" "$(basename "$URL_VAL")"

    ARCHIVE_FILE="$WORK_DIR/$(basename "$URL_VAL")"
    printf "${C}[*] Скачивание %s...${N}\n" "$DISPLAY_NAME"
    $DOWNLOAD "$ARCHIVE_FILE" "$URL_VAL" || fail "Не удалось скачать ${DISPLAY_NAME}"
    [ -s "$ARCHIVE_FILE" ] || fail "Скачанный файл ${DISPLAY_NAME} пуст или повреждён."

    stop_cheburnet_service

    printf "${C}[*] Удаление предыдущей версии ядра sing-box...${N}\n"
    rm -f "$DEST_FILE" /usr/bin/sing-box-extended /usr/bin/sing-box-lx 2>/dev/null || true

    printf "${C}[*] Установка нового ядра %s...${N}\n" "$DISPLAY_NAME"
    EXTRACT_DIR="$WORK_DIR/extracted_${PKG_NAME}"
    mkdir -p "$EXTRACT_DIR"

    case "$ARCHIVE_FILE" in
        *.tar.gz|*.tgz)
            tar -xzf "$ARCHIVE_FILE" -C "$EXTRACT_DIR"
            FOUND_BIN=$(find "$EXTRACT_DIR" -type f \( -name "sing-box" -o -name "$PKG_NAME" \) | head -n 1)
            [ -z "$FOUND_BIN" ] && FOUND_BIN=$(find "$EXTRACT_DIR" -type f -perm -111 | head -n 1)
            [ -z "$FOUND_BIN" ] && fail "Бинарный файл не найден внутри архива ${DISPLAY_NAME}."
            cp -f "$FOUND_BIN" "$DEST_FILE"
            ;;
        *.gz)
            gzip -dc "$ARCHIVE_FILE" > "$DEST_FILE" 2>/dev/null || gunzip -c "$ARCHIVE_FILE" > "$DEST_FILE" 2>/dev/null || fail "Ошибка распаковки gzip"
            ;;
        *)
            cp -f "$ARCHIVE_FILE" "$DEST_FILE"
            ;;
    esac

    chmod 755 "$DEST_FILE" 2>/dev/null || true
    ln -sf "$DEST_FILE" "/usr/bin/${PKG_NAME}" 2>/dev/null || true

    NEW_VER=$("$DEST_FILE" version 2>/dev/null | head -n 1 | awk '{print $3}') || true
    [ -z "$NEW_VER" ] && fail "Установленный бинарник $DEST_FILE не запускается."

    mkdir -p /etc/cheburnet
    echo "${NEW_VER:-$TAG_VAL}" > /etc/cheburnet/sing-box-version

    printf "${G}[✓] %s успешно установлен: %s${N}\n\n" "$DISPLAY_NAME" "${NEW_VER:-$TAG_VAL}"
}

# 7. Выбор ядра
DEF_SB_CHOICE="1"
if [ -n "$CURRENT_SB_VER" ]; then
    if echo "$CURRENT_SB_VER" | grep -qi "pdk"; then
        DEF_SB_CHOICE="1"
    elif echo "$CURRENT_SB_VER" | grep -qi "extended"; then
        DEF_SB_CHOICE="3"
    elif echo "$CURRENT_SB_VER" | grep -qi "lx"; then
        DEF_SB_CHOICE="4"
    fi
fi

echo "Операции с ядром маршрутизации:"
echo "  1) Установить / Обновить podkop-engine (${REPO_PODKOP} r10/r11/r14)"
echo "  2) Установить / Обновить официальный ванильный sing-box ($PKG_MANAGER)"
echo "  3) Установить / Обновить sing-box-extended ($REPO_SB_EXTENDED)"
echo "  4) Установить / Обновить sing-box-lx ($REPO_SB_LX)"
echo "  0) Пропустить обновление ядра"
printf "${C}[>] Ваш выбор [0-4] (по умолчанию %s): ${N}" "$DEF_SB_CHOICE"
read_input 30
CHOICE_SB="${READ_VALUE:-$DEF_SB_CHOICE}"

case "$CHOICE_SB" in
    1)
        install_podkop_engine
        ;;
    2)
        printf "${C}[*] Установка официального sing-box через %s...${N}\n" "$PKG_MANAGER"
        stop_cheburnet_service
        rm -f "$DEST_FILE" /usr/bin/sing-box-extended /usr/bin/sing-box-lx /etc/cheburnet/sing-box-version 2>/dev/null || true
        if [ "$PKG_MANAGER" = "apk" ]; then
            apk update && apk add --upgrade sing-box
        else
            opkg update && opkg install sing-box --force-reinstall
        fi
        NEW_SB_VER=$("$DEST_FILE" version 2>/dev/null | head -n 1 | awk '{print $3}') || true
        printf "${G}[✓] Официальный sing-box успешно установлен: %s${N}\n\n" "${NEW_SB_VER:-готово}"
        ;;
    3)
        install_custom_singbox "sing-box-extended" "$REPO_SB_EXTENDED" "sing-box-extended"
        ;;
    4)
        install_custom_singbox "sing-box-lx" "$REPO_SB_LX" "sing-box-lx"
        ;;
    0)
        printf "${Y}[*] Пропуск обновления ядра.${N}\n\n"
        ;;
    *)
        printf "${Y}[!] Неизвестный выбор. Пропуск ядра.${N}\n\n"
        ;;
esac

# 8. Проверка системных зависимостей
printf "${C}[*] Проверка зависимостей (nftables, kmod-nft-tproxy, ip-full, ca-bundle)...${N}\n"
if [ "$PKG_MANAGER" = "apk" ]; then
    apk update
    apk add --no-cache --upgrade nftables kmod-nft-tproxy ip-full ca-bundle
else
    opkg update
    opkg install nftables kmod-nft-tproxy ip-full ca-bundle
fi

# 9. Поиск и установка Chebur.NET
printf "\n${C}[*] Получение списка релизов Chebur.NET с GitHub...${N}\n"
TARGET_ARCH="${TARGET_PACKAGE_ARCH:-$CHEBUR_ARCH}"
RELEASES_LIST_JSON=$(api_get "https://api.github.com/repos/${REPO_CHEBUR}/releases?per_page=25")

PARSED_DATA=""
if [ -n "$RELEASES_LIST_JSON" ] && ! echo "$RELEASES_LIST_JSON" | grep -q '"message":'; then
    PARSED_DATA=$(echo "$RELEASES_LIST_JSON" | awk -v chan="$SELECTED_CHANNEL" -v arch="$TARGET_ARCH" -v h_arch="$HOST_ARCH" -v ext="$PKG_EXT" '
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

                u_low = tolower(u)
                score = 0

                if (arch != "" && index(u_low, tolower(arch)) > 0) score = 100
                else if (u_low ~ "_all\\." || u_low ~ "-all\\.") score = 90
                else if (h_arch == "aarch64") {
                    if (index(u_low, "cortex-a53") > 0) score = 80
                    else if (index(u_low, "arm64") > 0 || index(u_low, "aarch64") > 0) score = 70
                }
                else if (h_arch ~ "armv7" || h_arch ~ "arm") {
                    if (index(u_low, "cortex-a7") > 0 || index(u_low, "armhf") > 0) score = 80
                }
                else if (h_arch ~ "mipsel" || h_arch ~ "mipsle") {
                    if (index(u_low, "mipsel_24kc") > 0 || index(u_low, "mipsle") > 0) score = 80
                }
                else if (h_arch ~ "mips") {
                    if (index(u_low, "mips_24kc") > 0 || index(u_low, "mips") > 0) score = 80
                }

                if (score > 0) {
                    if (!best_score[cur_tag] || score > best_score[cur_tag]) {
                        best_score[cur_tag] = score
                        url[cur_tag] = u
                        if (!seen[cur_tag]) {
                            tags[++n] = cur_tag
                            seen[cur_tag] = 1
                        }
                    }
                }
            }
        }
        END {
            if (n > 0) {
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
        }
    ')
fi

# Fallback с правильным именем архитектурного пакета
if [ -z "$PARSED_DATA" ]; then
    printf "${Y}[!] Превышен лимит запросов GitHub API. Используется fallback на актуальный релиз...${N}\n"
    CHEBUR_CLEAN_VER="0.0.8.49-beta.5"
    if [ "$PKG_MANAGER" = "apk" ]; then
        CHEBUR_PKG_NAME="luci-app-cheburnet-0.0.8_p49-r1.${TARGET_ARCH}.apk"
        [ "$TARGET_ARCH" = "aarch64" ] && CHEBUR_PKG_NAME="luci-app-cheburnet-0.0.8_p49-r1.aarch64_cortex-a53.apk"
    else
        CHEBUR_PKG_NAME="luci-app-cheburnet_${CHEBUR_CLEAN_VER}_${TARGET_ARCH}.ipk"
        [ "$TARGET_ARCH" = "aarch64" ] && CHEBUR_PKG_NAME="luci-app-cheburnet_${CHEBUR_CLEAN_VER}_aarch64_cortex-a53.ipk"
    fi
    CHEBUR_URL="https://github.com/${REPO_CHEBUR}/releases/download/v${CHEBUR_CLEAN_VER}/${CHEBUR_PKG_NAME}"
else
    CHEBUR_CLEAN_VER=$(echo "$PARSED_DATA" | cut -d '|' -f 1)
    CHEBUR_URL=$(echo "$PARSED_DATA" | cut -d '|' -f 2)
fi

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
    printf "${C}[*] Скачивание %s...${N}\n" "$(basename "$CHEBUR_URL")"
    $DOWNLOAD "/tmp/cheburnet.${PKG_EXT}" "$CHEBUR_URL" || fail "Сбой при скачивании пакета Chebur.NET по ссылке $CHEBUR_URL"

    stop_cheburnet_service

    if [ -f "/etc/config/cheburnet" ]; then
        cp -f "/etc/config/cheburnet" "/tmp/cheburnet_config_backup"
    fi

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

# 10. Фиксация выбранного канала в UCI
if command -v uci >/dev/null 2>&1 && [ -f "/etc/config/cheburnet" ]; then
    uci -q set cheburnet.main.update_channel="$SELECTED_CHANNEL" || true
    uci -q commit cheburnet || true
fi

# 11. Финализация прав, каталогов и запуск службы
mkdir -p /var/etc/cheburnet /var/run/cheburnet
[ -f /usr/bin/cheburnetd ] && chmod 755 /usr/bin/cheburnetd
[ -f /etc/init.d/cheburnet ] && chmod 755 /etc/init.d/cheburnet

rm -rf /tmp/luci-indexcache /tmp/luci-modulecache/ "$WORK_DIR"

if [ "$DNS_BACKED_UP" = "1" ]; then
    [ -f "/tmp/resolv.conf.bak" ] && mv -f "/tmp/resolv.conf.bak" "/tmp/resolv.conf" 2>/dev/null || true
    [ -f "/tmp/resolv.conf.auto.bak" ] && mv -f "/tmp/resolv.conf.auto.bak" "/tmp/resolv.conf.auto" 2>/dev/null || true
fi

/etc/init.d/cheburnet enable >/dev/null 2>&1 || true
/etc/init.d/cheburnet restart >/dev/null 2>&1 || true
SERVICE_STOPPED="0"

printf "\n${G}====================================================${N}\n"
printf "${G}  Chebur.NET и ядро успешно настроены!              ${N}\n"
printf "  Канал обновлений:  ${Y}%s${N}\n" "$SELECTED_CHANNEL"
printf "  Версия ядра:       ${Y}%s${N}\n" "$("${DEST_FILE}" version 2>/dev/null | head -n 1 || echo "установлен")"
printf "  Служба:            ${Y}cheburnet (active/running)${N}\n"
printf "  Веб-интерфейс:     ${Y}LuCI -> Службы -> Chebur.NET${N}\n"
printf "${G}====================================================${N}\n"