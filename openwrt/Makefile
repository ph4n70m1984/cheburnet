include $(TOPDIR)/rules.mk

PKG_NAME:=cheburnet
PKG_VERSION:=1.0.0
PKG_RELEASE:=1

PKG_BUILD_DIR:=$(BUILD_DIR)/$(PKG_NAME)-$(PKG_VERSION)

include $(INCLUDE_DIR)/package.mk

define Package/cheburnet
  SECTION:=net
  CATEGORY:=Network
  TITLE:=Chebur.NET Transparent Proxy Routing Daemon
  DEPENDS:=+nftables +kmod-nft-tproxy +ip-full +ca-certificates
  URL:=https://github.com/your-repo/cheburnet
endef

define Package/cheburnet/description
  Transparent proxying daemon for OpenWrt supporting VLESS, Hysteria2,
  with dynamic switching between sing-box and Xray-core.
endef

define Build/Compile
	# Бинарник компилируется Go хостом заранее или через golang-package
endef

define Package/cheburnet/install
	$(INSTALL_DIR) $(1)/usr/bin
	$(INSTALL_BIN) $(PKG_BUILD_DIR)/cheburnetd $(1)/usr/bin/cheburnetd

	$(INSTALL_DIR) $(1)/etc/config
	$(INSTALL_CONF) ./files/etc/config/cheburnet $(1)/etc/config/cheburnet

	$(INSTALL_DIR) $(1)/etc/init.d
	$(INSTALL_BIN) ./files/etc/init.d/cheburnetd $(1)/etc/init.d/cheburnetd

	$(INSTALL_DIR) $(1)/www/luci-static/resources/view/cheburnet
	$(INSTALL_DATA) ./files/www/luci-static/resources/view/cheburnet/dashboard.js $(1)/www/luci-static/resources/view/cheburnet/dashboard.js
endef

$(eval $(call BuildPackage,cheburnet))