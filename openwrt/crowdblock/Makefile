include $(TOPDIR)/rules.mk

PKG_NAME:=crowdblock
PKG_VERSION:=0.1.0
PKG_RELEASE:=23

PKG_MAINTAINER:=Your Name <you@example.org>
PKG_LICENSE:=GPL-2.0-or-later

include $(INCLUDE_DIR)/package.mk

define Package/crowdblock
  SECTION:=net
  CATEGORY:=Network
  SUBMENU:=Firewall
  TITLE:=Crowd-sourced IP blocklist client
  DEPENDS:=+ucode +ucode-mod-fs +ucode-mod-uci \
           +ucode-mod-uloop +uclient-fetch +nftables \
           +coreutils-timeout
  PKGARCH:=all
endef

define Package/crowdblock/description
  Downloads a crowd-sourced list of attacking IP addresses and blocks
  those reported by at least N independent users, using an nftables
  set with per-element timeouts.
endef

define Package/crowdblock/conffiles
/etc/config/crowdblock
endef

# Pure ucode scripts - nothing to compile.
define Build/Compile
endef

define Package/crowdblock/install
	$(INSTALL_DIR) $(1)/usr/sbin
	$(INSTALL_BIN) ./files/crowdblock $(1)/usr/sbin/crowdblock

	$(INSTALL_DIR) $(1)/usr/share/ucode/crowdblock
	$(INSTALL_DATA) ./files/lib/*.uc $(1)/usr/share/ucode/crowdblock/
	sed -i "s/^export const VERSION = .*/export const VERSION = '$(PKG_VERSION)-r$(PKG_RELEASE)';/" $(1)/usr/share/ucode/crowdblock/api.uc

	$(INSTALL_DIR) $(1)/etc/init.d
	$(INSTALL_BIN) ./files/crowdblock.init $(1)/etc/init.d/crowdblock

	$(INSTALL_DIR) $(1)/etc/config
	$(INSTALL_CONF) ./files/crowdblock.config $(1)/etc/config/crowdblock
endef

$(eval $(call BuildPackage,crowdblock))
