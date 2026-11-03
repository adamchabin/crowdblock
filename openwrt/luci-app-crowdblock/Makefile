include $(TOPDIR)/rules.mk

PKG_NAME:=luci-app-crowdblock

LUCI_TITLE:=LuCI support for crowdblock
LUCI_DEPENDS:=+crowdblock
LUCI_PKGARCH:=all

# Set explicitly: luci.mk derives the version from git otherwise.
PKG_VERSION:=0.1.0
PKG_RELEASE:=19

PKG_MAINTAINER:=Your Name <you@example.org>
PKG_LICENSE:=GPL-2.0-or-later

include $(TOPDIR)/feeds/luci/luci.mk

$(eval $(call BuildPackage,luci-app-crowdblock))
