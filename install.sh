#!/bin/bash -x

VERBOSE=0
OS_NAME=
OS_VERSION=
OS_CODE_NAME=
ARCH=
export IP=
export DEBIAN_FRONTEND=noninteractive
[[ -z "$CREATE_AMI" ]] && export CREATE_AMI
[[ -z "$DB_ENGINE" ]] && export DB_ENGINE="MYSQL_8.4"

export IS_LXC=0
if grep -q container=lxc "/proc/1/environ"; then
  export IS_LXC=1
fi

RED_TEXT_COLOR=`tput setaf 1`
GREEN_TEXT_COLOR=`tput setaf 2`
YELLOW_TEXT_COLOR=`tput setaf 3`
RESET_TEXT_COLOR=`tput sgr0`

if [ -z "${SWAP}" ]; then
  SWAP=true
fi

die()
{
  /bin/echo -e "ERROR: $*" >&2
  exit 1
}

verbose()
{
  if [ "$VERBOSE" -eq "1" ]; then
    echo "$@" >&2
  fi
}

setOSInfo()
{
  [ -e '/bin/uname' ] && uname='/bin/uname' || uname='/usr/bin/uname'
  ARCH=`uname -m`
  OPERATING_SYSTEM=`uname -s`
  if [ "$OPERATING_SYSTEM" = 'Linux' ]; then
    if [ -e '/etc/debian_version' ]; then
      if [ -e '/etc/lsb-release' ]; then
        . /etc/lsb-release
        OS_NAME=$DISTRIB_ID
        OS_CODE_NAME=$(awk -F'=' '/VERSION_CODENAME/ {print $2}' /etc/os-release)
        OS_VERSION=$DISTRIB_RELEASE
      else
        OS_NAME='Debian'
        OS_CODE_NAME=$(awk -F= '/VERSION_CODENAME/{print $2}' /etc/os-release)
        DEBIAN_VERSION=$(cat /etc/debian_version)
        OS_VERSION=`echo $DEBIAN_VERSION | cut -d "." -f -1`
      fi
    else
      die "Unable to detect Debian or Ubuntu."
    fi
  else
    die "Operating System needs to be Linux."
  fi

  verbose "Architecture: $ARCH"
  verbose "OS Name: $OS_NAME"
  verbose "OS Version: $OS_VERSION"
}

checkRequirements()
{
  apt update
  apt -y install lsof
  checkOperatingSystem
  checkPortConflicts
  checkDatabaseEngine
  checkIfHostnameResolves
  checkRootPartitionSize
}

checkOperatingSystem()
{
  if [ "$OS_NAME" = "Debian" ] || [ "$OS_NAME" = "Ubuntu" ]; then
    if [ "$OS_NAME" = "Debian" ]; then
      if [ "$OS_VERSION" != "11" ] && [ "$OS_VERSION" != "12" ] && [ "$OS_VERSION" != "13" ]; then
        die "Debian 11, 12 and 13 are supported."
      fi
    else
      if [ "$OS_VERSION" != "22.04" ] && [ "$OS_VERSION" != "24.04" ]; then
        die "Ubuntu 22.04 LTS, 24.04 LTS are supported."
      fi
    fi
  else
    die "Operating System needs to be Debian or Ubuntu."
  fi
}

checkPortConflicts()
{
  local OPEN_PORTS=$(lsof -i:80 -i:443 -i:3306 -P -n -sTCP:LISTEN)
  if [ -n "${OPEN_PORTS}" ]; then
    die "Your system already has services running on port 80, 443 or 3306."
  fi
}

checkDatabaseEngine() {
  if [ "$OS_NAME" = "Debian" ]; then
    case $OS_VERSION in
      "11")
        case $DB_ENGINE in
          "MYSQL_5.7" | "MYSQL_8.0" | "MARIADB_10.6" | "MARIADB_10.11" | "MARIADB_11.4" | "MARIADB_11.8")
            echo "Database Engine: $DB_ENGINE"
          ;;
          *)
            die "Database Engine $DB_ENGINE not supported."
          ;;
        esac
      ;;
      "12")
        case $DB_ENGINE in
          "MYSQL_8.4" | "MYSQL_8.0" | "MARIADB_10.11" | "MARIADB_11.4" | "MARIADB_11.8")
            echo "Database Engine: $DB_ENGINE"
          ;;
          *)
            die "Database Engine $DB_ENGINE not supported."
          ;;
        esac
      ;;
      "13")
        case $DB_ENGINE in
          "MYSQL_8.4" | "MYSQL_8.0" | "MARIADB_11.8")
            echo "Database Engine: $DB_ENGINE"
          ;;
          *)
            die "Database Engine $DB_ENGINE not supported."
          ;;
        esac
        ;;
      *)
        die "Unsupported Debian version: $OS_VERSION"
      ;;
    esac
  elif [ "$OS_NAME" = "Ubuntu" ]; then
    case $OS_VERSION in
      "22.04")
        case $DB_ENGINE in
          "MYSQL_8.0" | "MARIADB_10.6" | "MARIADB_10.11" | "MARIADB_11.4" | "MARIADB_11.8")
            echo "Database Engine: $DB_ENGINE"
          ;;
          *)
            die "Database Engine $DB_ENGINE not supported."
          ;;
        esac
      ;;
      "24.04")
        case $DB_ENGINE in
          "MYSQL_8.4" | "MYSQL_8.0" | "MARIADB_10.11" | "MARIADB_11.4" | "MARIADB_11.8")
            echo "Database Engine: $DB_ENGINE"
          ;;
          *)
            die "Database Engine $DB_ENGINE not supported."
          ;;
        esac
      ;;
      *)
        die "Unsupported Ubuntu version: $OS_VERSION"
      ;;
    esac
  else
    die "Unsupported OS: $OS_NAME"
  fi
}

checkIfHostnameResolves()
{
  local LOCAL_IP=$(getent hosts "$HOSTNAME" | awk '{print $1}')
  if [ -z "${LOCAL_IP}" ]; then
    die "Hostname $HOSTNAME does not resolve. Set a hosts entry in: /etc/hosts"
  fi
}

checkRootPartitionSize()
{
  # In KB
  local ROOT_PARTITION=$(df --output=avail / | sed '1d')
  if [ $ROOT_PARTITION -lt 6000000 ]; then
    die "At least 6GB of free hard disk space is required"
  fi
}

removeUnnecessaryPackages()
{
  apt -y --purge remove mysql* &> /dev/null
}

setIp()
{
  IP=$(curl -sk --connect-timeout 10 --retry 3 --retry-delay 0 https://d3qnd54q8gb3je.cloudfront.net/)
  IP=$(echo "$IP" | cut -d"," -f1)
}

setupRequiredPackages()
{
  apt -y upgrade
  apt -y install gnupg gpgv apt-transport-https debsums chrony redis-server
  DEBIAN_FRONTEND=noninteractive apt-get install -y postfix
  if [ "$SWAP" != false ] ; then
    echo "CONF_SWAPFILE=/home/.swap" > /etc/dphys-swapfile
    echo "CONF_SWAPSIZE=2048" >> /etc/dphys-swapfile
    echo "CONF_MAXSWAP=2048" >> /etc/dphys-swapfile
    DEBIAN_FRONTEND=noninteractive apt-get -y -o Dpkg::Options::=--force-confdef -o Dpkg::Options::=--force-confnew install dphys-swapfile
  fi
}

generateLocales()
{
  apt -y install locales locales-all
  /usr/sbin/locale-gen en_US && /usr/sbin/locale-gen en_US.UTF-8
}

addAptSourceList()
{
  curl -fsSL https://d17k9fuiwb52nc.cloudfront.net/key.gpg | sudo gpg --yes --dearmor -o /etc/apt/trusted.gpg.d/cloudpanel-keyring.gpg

  if [ "$ARCH" = "aarch64" ]; then
    ORIGIN="d2xpdm4jldf31f.cloudfront.net"
  else
    ORIGIN="d17k9fuiwb52nc.cloudfront.net"
  fi

  CLOUDPANEL_SOURCE_LIST=$(cat <<-END
deb https://$ORIGIN/ $OS_CODE_NAME main
deb https://$ORIGIN/ $OS_CODE_NAME nginx
deb https://$ORIGIN/ $OS_CODE_NAME php-7.1
deb https://$ORIGIN/ $OS_CODE_NAME php-7.2
deb https://$ORIGIN/ $OS_CODE_NAME php-7.3
deb https://$ORIGIN/ $OS_CODE_NAME php-7.4
deb https://$ORIGIN/ $OS_CODE_NAME php-8.0
deb https://$ORIGIN/ $OS_CODE_NAME php-8.1
deb https://$ORIGIN/ $OS_CODE_NAME php-8.2
deb https://$ORIGIN/ $OS_CODE_NAME php-8.3
deb https://$ORIGIN/ $OS_CODE_NAME php-8.4
deb https://$ORIGIN/ $OS_CODE_NAME php-8.5
deb https://$ORIGIN/ $OS_CODE_NAME proftpd
deb https://$ORIGIN/ $OS_CODE_NAME varnish-7
END
)

CLOUDPANEL_APT_PREFERENCES=$(cat <<-END
Package: *
Pin: origin $ORIGIN
Pin-Priority: 1000
END
)

  echo -e "$CLOUDPANEL_SOURCE_LIST" > /etc/apt/sources.list.d/packages.cloudpanel.io.list
  echo -e "$CLOUDPANEL_APT_PREFERENCES" > /etc/apt/preferences.d/00packages.cloudpanel.io.pref
  apt -y update
}

installMySQL() {
  addAptSourceList

  if [ "$OS_NAME" = "Debian" ]; then
    case $OS_VERSION in
      "11")
        case $DB_ENGINE in
          "MYSQL_5.7")
            echo "deb https://$ORIGIN/ $OS_CODE_NAME percona-server-server-5.7" > /etc/apt/sources.list.d/percona-mysql.list
            apt -y update
            DEBIAN_FRONTEND=noninteractive apt -y install percona-server-client-5.7 percona-server-server-5.7
            ;;
          "MYSQL_8.0")
            echo "deb https://$ORIGIN/ $OS_CODE_NAME percona-server-server-8.0" > /etc/apt/sources.list.d/percona-mysql.list
            apt -y update
            DEBIAN_FRONTEND=noninteractive apt -y install percona-server-client percona-server-server
            ;;
          "MARIADB_10.6")
            wget -qO- https://mariadb.org/mariadb_release_signing_key.asc | gpg --dearmor > /etc/apt/trusted.gpg.d/mariadb.gpg
            echo "deb [arch=amd64,arm64] https://mirror.mariadb.org/repo/10.6/debian bullseye main" > /etc/apt/sources.list.d/mariadb.list
            apt -y update
            apt -y install mariadb-server
            ;;
          "MARIADB_10.11")
            wget -qO- https://mariadb.org/mariadb_release_signing_key.asc | gpg --dearmor > /etc/apt/trusted.gpg.d/mariadb.gpg
            echo "deb [arch=amd64,arm64] https://mirror.mariadb.org/repo/10.11/debian bullseye main" > /etc/apt/sources.list.d/mariadb.list
            apt -y update
            apt -y install mariadb-server
            ;;
          "MARIADB_11.4")
            wget -qO- https://mariadb.org/mariadb_release_signing_key.asc | gpg --dearmor > /etc/apt/trusted.gpg.d/mariadb.gpg
            echo "deb [arch=amd64,arm64] https://mirror.mariadb.org/repo/11.4/debian bullseye main" > /etc/apt/sources.list.d/mariadb.list
            apt -y update
            apt -y install mariadb-server
            ln -sf /usr/bin/mariadb /usr/bin/mysql
            ln -sf /usr/bin/mariadb-access /usr/bin/mysqlaccess
            ln -sf /usr/bin/mariadb-admin /usr/bin/mysqladmin
            ln -sf /usr/bin/mariadb-check /usr/bin/mysqlanalyze
            ln -sf /usr/bin/mariadb-binlog /usr/bin/mysqlbinlog
            ln -sf /usr/bin/mariadb-check /usr/bin/mysqlcheck
            ln -sf /usr/bin/mariadb-convert-table-format /usr/bin/mysql_convert_table_format
            ln -sf /usr/bin/mariadbd-multi /usr/bin/mysqld_multi
            ln -sf /usr/bin/mariadbd-safe /usr/bin/mysqld_safe
            ln -sf /usr/bin/mariadbd-safe-helper /usr/bin/mysqld_safe_helper
            ln -sf /usr/bin/mariadb-dump /usr/bin/mysqldump
            ln -sf /usr/bin/mariadb-dumpslow /usr/bin/mysqldumpslow
            ln -sf /usr/bin/mariadb-find-rows /usr/bin/mysql_find_rows
            ln -sf /usr/bin/mariadb-fix-extensions /usr/bin/mysql_fix_extensions
            ln -sf /usr/bin/mariadb-hotcopy /usr/bin/mysqlhotcopy
            ln -sf /usr/bin/mariadb-import /usr/bin/mysqlimport
            ln -sf /usr/bin/mariadb-install-db /usr/bin/mysql_install_db
            ln -sf /usr/bin/mariadb-check /usr/bin/mysqloptimize
            ln -sf /usr/bin/mariadb-plugin /usr/bin/mysql_plugin
            ln -sf /usr/bin/mariadb-check /usr/bin/mysqlrepair
            ln -sf /usr/bin/mariadb-report /usr/bin/mysqlreport
            ln -sf /usr/bin/mariadb-secure-installation /usr/bin/mysql_secure_installation
            ln -sf /usr/bin/mariadb-setpermission /usr/bin/mysql_setpermission
            ln -sf /usr/bin/mariadb-show /usr/bin/mysqlshow
            ln -sf /usr/bin/mariadb-slap /usr/bin/mysqlslap
            ln -sf /usr/bin/mariadb-tzinfo-to-sql /usr/bin/mysql_tzinfo_to_sql
            ln -sf /usr/bin/mariadb-upgrade /usr/bin/mysql_upgrade
            ln -sf /usr/bin/mariadb-waitpid /usr/bin/mysql_waitpid
            ;;
          "MARIADB_11.8")
            wget -qO- https://mariadb.org/mariadb_release_signing_key.asc | gpg --dearmor > /etc/apt/trusted.gpg.d/mariadb.gpg
            echo "deb [arch=amd64,arm64] https://mirror.mariadb.org/repo/11.8/debian bullseye main" > /etc/apt/sources.list.d/mariadb.list
            apt -y update
            apt -y install mariadb-server
            ln -sf /usr/bin/mariadb /usr/bin/mysql
            ln -sf /usr/bin/mariadb-access /usr/bin/mysqlaccess
            ln -sf /usr/bin/mariadb-admin /usr/bin/mysqladmin
            ln -sf /usr/bin/mariadb-check /usr/bin/mysqlanalyze
            ln -sf /usr/bin/mariadb-binlog /usr/bin/mysqlbinlog
            ln -sf /usr/bin/mariadb-check /usr/bin/mysqlcheck
            ln -sf /usr/bin/mariadb-convert-table-format /usr/bin/mysql_convert_table_format
            ln -sf /usr/bin/mariadbd-multi /usr/bin/mysqld_multi
            ln -sf /usr/bin/mariadbd-safe /usr/bin/mysqld_safe
            ln -sf /usr/bin/mariadbd-safe-helper /usr/bin/mysqld_safe_helper
            ln -sf /usr/bin/mariadb-dump /usr/bin/mysqldump
            ln -sf /usr/bin/mariadb-dumpslow /usr/bin/mysqldumpslow
            ln -sf /usr/bin/mariadb-find-rows /usr/bin/mysql_find_rows
            ln -sf /usr/bin/mariadb-fix-extensions /usr/bin/mysql_fix_extensions
            ln -sf /usr/bin/mariadb-hotcopy /usr/bin/mysqlhotcopy
            ln -sf /usr/bin/mariadb-import /usr/bin/mysqlimport
            ln -sf /usr/bin/mariadb-install-db /usr/bin/mysql_install_db
            ln -sf /usr/bin/mariadb-check /usr/bin/mysqloptimize
            ln -sf /usr/bin/mariadb-plugin /usr/bin/mysql_plugin
            ln -sf /usr/bin/mariadb-check /usr/bin/mysqlrepair
            ln -sf /usr/bin/mariadb-report /usr/bin/mysqlreport
            ln -sf /usr/bin/mariadb-secure-installation /usr/bin/mysql_secure_installation
            ln -sf /usr/bin/mariadb-setpermission /usr/bin/mysql_setpermission
            ln -sf /usr/bin/mariadb-show /usr/bin/mysqlshow
            ln -sf /usr/bin/mariadb-slap /usr/bin/mysqlslap
            ln -sf /usr/bin/mariadb-tzinfo-to-sql /usr/bin/mysql_tzinfo_to_sql
            ln -sf /usr/bin/mariadb-upgrade /usr/bin/mysql_upgrade
            ln -sf /usr/bin/mariadb-waitpid /usr/bin/mysql_waitpid
            ;;
          *)
            die "Database Engine $DB_ENGINE not supported."
            ;;
        esac
        ;;
      "12")
        case $DB_ENGINE in
          "MYSQL_8.4")
            #See: https://docs.percona.com/percona-server/8.4/apt-repo.html
            curl -o /tmp/percona-release_latest.generic_all.deb https://repo.percona.com/apt/percona-release_latest.generic_all.deb
            dpkg -i /tmp/percona-release_latest.generic_all.deb
            percona-release enable-only ps-84-lts release
            percona-release enable tools release
            apt -y update
            DEBIAN_FRONTEND=noninteractive apt -y install percona-server-server
            rm -f /tmp/percona-release_latest.generic_all.deb
            ;;
          "MYSQL_8.0")
            echo "deb https://$ORIGIN/ $OS_CODE_NAME percona-server-server-8.0" > /etc/apt/sources.list.d/percona-mysql.list
            apt -y update
            DEBIAN_FRONTEND=noninteractive apt -y install percona-server-client percona-server-server
            ;;
          "MARIADB_10.11")
            apt -y update
            apt -y install mariadb-server
            ;;
          "MARIADB_11.4")
            wget -qO- https://mariadb.org/mariadb_release_signing_key.asc | gpg --dearmor > /etc/apt/trusted.gpg.d/mariadb.gpg
            echo "deb [arch=amd64,arm64] https://mirror.mariadb.org/repo/11.4/debian bookworm main" > /etc/apt/sources.list.d/mariadb.list
            apt -y update
            apt -y install mariadb-server
            ln -sf /usr/bin/mariadb /usr/bin/mysql
            ln -sf /usr/bin/mariadb-access /usr/bin/mysqlaccess
            ln -sf /usr/bin/mariadb-admin /usr/bin/mysqladmin
            ln -sf /usr/bin/mariadb-check /usr/bin/mysqlanalyze
            ln -sf /usr/bin/mariadb-binlog /usr/bin/mysqlbinlog
            ln -sf /usr/bin/mariadb-check /usr/bin/mysqlcheck
            ln -sf /usr/bin/mariadb-convert-table-format /usr/bin/mysql_convert_table_format
            ln -sf /usr/bin/mariadbd-multi /usr/bin/mysqld_multi
            ln -sf /usr/bin/mariadbd-safe /usr/bin/mysqld_safe
            ln -sf /usr/bin/mariadbd-safe-helper /usr/bin/mysqld_safe_helper
            ln -sf /usr/bin/mariadb-dump /usr/bin/mysqldump
            ln -sf /usr/bin/mariadb-dumpslow /usr/bin/mysqldumpslow
            ln -sf /usr/bin/mariadb-find-rows /usr/bin/mysql_find_rows
            ln -sf /usr/bin/mariadb-fix-extensions /usr/bin/mysql_fix_extensions
            ln -sf /usr/bin/mariadb-hotcopy /usr/bin/mysqlhotcopy
            ln -sf /usr/bin/mariadb-import /usr/bin/mysqlimport
            ln -sf /usr/bin/mariadb-install-db /usr/bin/mysql_install_db
            ln -sf /usr/bin/mariadb-check /usr/bin/mysqloptimize
            ln -sf /usr/bin/mariadb-plugin /usr/bin/mysql_plugin
            ln -sf /usr/bin/mariadb-check /usr/bin/mysqlrepair
            ln -sf /usr/bin/mariadb-report /usr/bin/mysqlreport
            ln -sf /usr/bin/mariadb-secure-installation /usr/bin/mysql_secure_installation
            ln -sf /usr/bin/mariadb-setpermission /usr/bin/mysql_setpermission
            ln -sf /usr/bin/mariadb-show /usr/bin/mysqlshow
            ln -sf /usr/bin/mariadb-slap /usr/bin/mysqlslap
            ln -sf /usr/bin/mariadb-tzinfo-to-sql /usr/bin/mysql_tzinfo_to_sql
            ln -sf /usr/bin/mariadb-upgrade /usr/bin/mysql_upgrade
            ln -sf /usr/bin/mariadb-waitpid /usr/bin/mysql_waitpid
            ;;
          "MARIADB_11.8")
            wget -qO- https://mariadb.org/mariadb_release_signing_key.asc | gpg --dearmor > /etc/apt/trusted.gpg.d/mariadb.gpg
            echo "deb [arch=amd64,arm64] https://mirror.mariadb.org/repo/11.8/debian bookworm main" > /etc/apt/sources.list.d/mariadb.list
            apt -y update
            apt -y install mariadb-server
            ln -sf /usr/bin/mariadb /usr/bin/mysql
            ln -sf /usr/bin/mariadb-access /usr/bin/mysqlaccess
            ln -sf /usr/bin/mariadb-admin /usr/bin/mysqladmin
            ln -sf /usr/bin/mariadb-check /usr/bin/mysqlanalyze
            ln -sf /usr/bin/mariadb-binlog /usr/bin/mysqlbinlog
            ln -sf /usr/bin/mariadb-check /usr/bin/mysqlcheck
            ln -sf /usr/bin/mariadb-convert-table-format /usr/bin/mysql_convert_table_format
            ln -sf /usr/bin/mariadbd-multi /usr/bin/mysqld_multi
            ln -sf /usr/bin/mariadbd-safe /usr/bin/mysqld_safe
            ln -sf /usr/bin/mariadbd-safe-helper /usr/bin/mysqld_safe_helper
            ln -sf /usr/bin/mariadb-dump /usr/bin/mysqldump
            ln -sf /usr/bin/mariadb-dumpslow /usr/bin/mysqldumpslow
            ln -sf /usr/bin/mariadb-find-rows /usr/bin/mysql_find_rows
            ln -sf /usr/bin/mariadb-fix-extensions /usr/bin/mysql_fix_extensions
            ln -sf /usr/bin/mariadb-hotcopy /usr/bin/mysqlhotcopy
            ln -sf /usr/bin/mariadb-import /usr/bin/mysqlimport
            ln -sf /usr/bin/mariadb-install-db /usr/bin/mysql_install_db
            ln -sf /usr/bin/mariadb-check /usr/bin/mysqloptimize
            ln -sf /usr/bin/mariadb-plugin /usr/bin/mysql_plugin
            ln -sf /usr/bin/mariadb-check /usr/bin/mysqlrepair
            ln -sf /usr/bin/mariadb-report /usr/bin/mysqlreport
            ln -sf /usr/bin/mariadb-secure-installation /usr/bin/mysql_secure_installation
            ln -sf /usr/bin/mariadb-setpermission /usr/bin/mysql_setpermission
            ln -sf /usr/bin/mariadb-show /usr/bin/mysqlshow
            ln -sf /usr/bin/mariadb-slap /usr/bin/mysqlslap
            ln -sf /usr/bin/mariadb-tzinfo-to-sql /usr/bin/mysql_tzinfo_to_sql
            ln -sf /usr/bin/mariadb-upgrade /usr/bin/mysql_upgrade
            ln -sf /usr/bin/mariadb-waitpid /usr/bin/mysql_waitpid
            ;;
          *)
            die "Database Engine $DB_ENGINE not supported."
            ;;
        esac
        ;;
      "13")
        case $DB_ENGINE in
          "MYSQL_8.4")
            curl -o /tmp/percona-release_latest.generic_all.deb https://repo.percona.com/apt/percona-release_latest.generic_all.deb
            dpkg -i /tmp/percona-release_latest.generic_all.deb
            percona-release enable-only ps-84-lts release
            percona-release enable tools release
            apt -y update
            DEBIAN_FRONTEND=noninteractive apt -y install percona-server-server
            rm -f /tmp/percona-release_latest.generic_all.deb
            ;;
          "MYSQL_8.0")
            echo "deb https://$ORIGIN/ $OS_CODE_NAME percona-server-server-8.0" > /etc/apt/sources.list.d/percona-mysql.list
            apt -y update
            DEBIAN_FRONTEND=noninteractive apt -y install percona-server-client percona-server-server
            ;;
          "MARIADB_11.8")
            wget -qO- https://mariadb.org/mariadb_release_signing_key.asc | gpg --dearmor > /etc/apt/trusted.gpg.d/mariadb.gpg
            echo "deb [arch=amd64,arm64] https://mirror.mariadb.org/repo/11.8/debian trixie main" > /etc/apt/sources.list.d/mariadb.list
            apt -y update
            apt -y install mariadb-server
            ln -sf /usr/bin/mariadb /usr/bin/mysql
            ln -sf /usr/bin/mariadb-access /usr/bin/mysqlaccess
            ln -sf /usr/bin/mariadb-admin /usr/bin/mysqladmin
            ln -sf /usr/bin/mariadb-check /usr/bin/mysqlanalyze
            ln -sf /usr/bin/mariadb-binlog /usr/bin/mysqlbinlog
            ln -sf /usr/bin/mariadb-check /usr/bin/mysqlcheck
            ln -sf /usr/bin/mariadb-convert-table-format /usr/bin/mysql_convert_table_format
            ln -sf /usr/bin/mariadbd-multi /usr/bin/mysqld_multi
            ln -sf /usr/bin/mariadbd-safe /usr/bin/mysqld_safe
            ln -sf /usr/bin/mariadbd-safe-helper /usr/bin/mysqld_safe_helper
            ln -sf /usr/bin/mariadb-dump /usr/bin/mysqldump
            ln -sf /usr/bin/mariadb-dumpslow /usr/bin/mysqldumpslow
            ln -sf /usr/bin/mariadb-find-rows /usr/bin/mysql_find_rows
            ln -sf /usr/bin/mariadb-fix-extensions /usr/bin/mysql_fix_extensions
            ln -sf /usr/bin/mariadb-hotcopy /usr/bin/mysqlhotcopy
            ln -sf /usr/bin/mariadb-import /usr/bin/mysqlimport
            ln -sf /usr/bin/mariadb-install-db /usr/bin/mysql_install_db
            ln -sf /usr/bin/mariadb-check /usr/bin/mysqloptimize
            ln -sf /usr/bin/mariadb-plugin /usr/bin/mysql_plugin
            ln -sf /usr/bin/mariadb-check /usr/bin/mysqlrepair
            ln -sf /usr/bin/mariadb-report /usr/bin/mysqlreport
            ln -sf /usr/bin/mariadb-secure-installation /usr/bin/mysql_secure_installation
            ln -sf /usr/bin/mariadb-setpermission /usr/bin/mysql_setpermission
            ln -sf /usr/bin/mariadb-show /usr/bin/mysqlshow
            ln -sf /usr/bin/mariadb-slap /usr/bin/mysqlslap
            ln -sf /usr/bin/mariadb-tzinfo-to-sql /usr/bin/mysql_tzinfo_to_sql
            ln -sf /usr/bin/mariadb-upgrade /usr/bin/mysql_upgrade
            ln -sf /usr/bin/mariadb-waitpid /usr/bin/mysql_waitpid
            ;;
          *)
            die "Database Engine $DB_ENGINE not supported."
          ;;
        esac
       ;;
       *)
        die "Unsupported Debian version: $OS_VERSION."
        ;;
    esac
  elif [ "$OS_NAME" = "Ubuntu" ]; then
    case $OS_VERSION in
      "22.04")
        case $DB_ENGINE in
          "MYSQL_8.0")
            apt -y update
            DEBIAN_FRONTEND=noninteractive apt -y install mysql-client-8.0 mysql-server-8.0
            ;;
          "MARIADB_10.6")
            apt -y update
            apt -y install mariadb-server
            ;;
          "MARIADB_10.11")
            wget -qO- https://mariadb.org/mariadb_release_signing_key.asc | gpg --dearmor > /etc/apt/trusted.gpg.d/mariadb.gpg
            echo "deb [arch=amd64,arm64] https://mirror.mariadb.org/repo/10.11/ubuntu/ jammy main" > /etc/apt/sources.list.d/mariadb.list
            apt -y update
            apt -y install mariadb-server
            ;;
          "MARIADB_11.4")
            wget -qO- https://mariadb.org/mariadb_release_signing_key.asc | gpg --dearmor > /etc/apt/trusted.gpg.d/mariadb.gpg
            echo "deb [arch=amd64,arm64] https://mirror.mariadb.org/repo/11.4/ubuntu jammy main" > /etc/apt/sources.list.d/mariadb.list
            apt -y update
            apt -y install mariadb-server
            ;;
          "MARIADB_11.8")
            wget -qO- https://mariadb.org/mariadb_release_signing_key.asc | gpg --dearmor > /etc/apt/trusted.gpg.d/mariadb.gpg
            echo "deb [arch=amd64,arm64] https://mirror.mariadb.org/repo/11.8/ubuntu jammy main" > /etc/apt/sources.list.d/mariadb.list
            apt -y update
            apt -y install mariadb-server
            ln -sf /usr/bin/mariadb /usr/bin/mysql
            ln -sf /usr/bin/mariadb-access /usr/bin/mysqlaccess
            ln -sf /usr/bin/mariadb-admin /usr/bin/mysqladmin
            ln -sf /usr/bin/mariadb-check /usr/bin/mysqlanalyze
            ln -sf /usr/bin/mariadb-binlog /usr/bin/mysqlbinlog
            ln -sf /usr/bin/mariadb-check /usr/bin/mysqlcheck
            ln -sf /usr/bin/mariadb-convert-table-format /usr/bin/mysql_convert_table_format
            ln -sf /usr/bin/mariadbd-multi /usr/bin/mysqld_multi
            ln -sf /usr/bin/mariadbd-safe /usr/bin/mysqld_safe
            ln -sf /usr/bin/mariadbd-safe-helper /usr/bin/mysqld_safe_helper
            ln -sf /usr/bin/mariadb-dump /usr/bin/mysqldump
            ln -sf /usr/bin/mariadb-dumpslow /usr/bin/mysqldumpslow
            ln -sf /usr/bin/mariadb-find-rows /usr/bin/mysql_find_rows
            ln -sf /usr/bin/mariadb-fix-extensions /usr/bin/mysql_fix_extensions
            ln -sf /usr/bin/mariadb-hotcopy /usr/bin/mysqlhotcopy
            ln -sf /usr/bin/mariadb-import /usr/bin/mysqlimport
            ln -sf /usr/bin/mariadb-install-db /usr/bin/mysql_install_db
            ln -sf /usr/bin/mariadb-check /usr/bin/mysqloptimize
            ln -sf /usr/bin/mariadb-plugin /usr/bin/mysql_plugin
            ln -sf /usr/bin/mariadb-check /usr/bin/mysqlrepair
            ln -sf /usr/bin/mariadb-report /usr/bin/mysqlreport
            ln -sf /usr/bin/mariadb-secure-installation /usr/bin/mysql_secure_installation
            ln -sf /usr/bin/mariadb-setpermission /usr/bin/mysql_setpermission
            ln -sf /usr/bin/mariadb-show /usr/bin/mysqlshow
            ln -sf /usr/bin/mariadb-slap /usr/bin/mysqlslap
            ln -sf /usr/bin/mariadb-tzinfo-to-sql /usr/bin/mysql_tzinfo_to_sql
            ln -sf /usr/bin/mariadb-upgrade /usr/bin/mysql_upgrade
            ln -sf /usr/bin/mariadb-waitpid /usr/bin/mysql_waitpid
            ;;
          *)
            die "Database Engine $DB_ENGINE not supported."
            ;;
        esac
        ;;
      "24.04")
        case $DB_ENGINE in
          "MYSQL_8.4")
            #See: https://docs.percona.com/percona-server/8.4/apt-repo.html
            curl -o /tmp/percona-release_latest.generic_all.deb https://repo.percona.com/apt/percona-release_latest.generic_all.deb
            dpkg -i /tmp/percona-release_latest.generic_all.deb
            percona-release enable-only ps-84-lts release
            percona-release enable tools release
            apt -y update
            DEBIAN_FRONTEND=noninteractive apt -y install percona-server-server
            rm -f /tmp/percona-release_latest.generic_all.deb
            ;;
          "MYSQL_8.0")
            echo "deb https://$ORIGIN/ $OS_CODE_NAME percona-server-server-8.0" > /etc/apt/sources.list.d/percona-mysql.list
            apt -y update
            DEBIAN_FRONTEND=noninteractive apt -y install percona-server-client percona-server-server
            ;;
          "MARIADB_10.11")
            apt -y update
            apt -y install mariadb-server
            ;;
          "MARIADB_11.4")
            wget -qO- https://mariadb.org/mariadb_release_signing_key.asc | gpg --dearmor > /etc/apt/trusted.gpg.d/mariadb.gpg
            echo "deb [arch=amd64,arm64] https://mirror.mariadb.org/repo/11.4/ubuntu noble main" > /etc/apt/sources.list.d/mariadb.list
            apt -y update
            apt -y install mariadb-server
            ;;
          "MARIADB_11.8")
            wget -qO- https://mariadb.org/mariadb_release_signing_key.asc | gpg --dearmor > /etc/apt/trusted.gpg.d/mariadb.gpg
            echo "deb [arch=amd64,arm64] https://mirror.mariadb.org/repo/11.8/ubuntu noble main" > /etc/apt/sources.list.d/mariadb.list
            apt -y update
            apt -y install mariadb-server
            ln -sf /usr/bin/mariadb /usr/bin/mysql
            ln -sf /usr/bin/mariadb-access /usr/bin/mysqlaccess
            ln -sf /usr/bin/mariadb-admin /usr/bin/mysqladmin
            ln -sf /usr/bin/mariadb-check /usr/bin/mysqlanalyze
            ln -sf /usr/bin/mariadb-binlog /usr/bin/mysqlbinlog
            ln -sf /usr/bin/mariadb-check /usr/bin/mysqlcheck
            ln -sf /usr/bin/mariadb-convert-table-format /usr/bin/mysql_convert_table_format
            ln -sf /usr/bin/mariadbd-multi /usr/bin/mysqld_multi
            ln -sf /usr/bin/mariadbd-safe /usr/bin/mysqld_safe
            ln -sf /usr/bin/mariadbd-safe-helper /usr/bin/mysqld_safe_helper
            ln -sf /usr/bin/mariadb-dump /usr/bin/mysqldump
            ln -sf /usr/bin/mariadb-dumpslow /usr/bin/mysqldumpslow
            ln -sf /usr/bin/mariadb-find-rows /usr/bin/mysql_find_rows
            ln -sf /usr/bin/mariadb-fix-extensions /usr/bin/mysql_fix_extensions
            ln -sf /usr/bin/mariadb-hotcopy /usr/bin/mysqlhotcopy
            ln -sf /usr/bin/mariadb-import /usr/bin/mysqlimport
            ln -sf /usr/bin/mariadb-install-db /usr/bin/mysql_install_db
            ln -sf /usr/bin/mariadb-check /usr/bin/mysqloptimize
            ln -sf /usr/bin/mariadb-plugin /usr/bin/mysql_plugin
            ln -sf /usr/bin/mariadb-check /usr/bin/mysqlrepair
            ln -sf /usr/bin/mariadb-report /usr/bin/mysqlreport
            ln -sf /usr/bin/mariadb-secure-installation /usr/bin/mysql_secure_installation
            ln -sf /usr/bin/mariadb-setpermission /usr/bin/mysql_setpermission
            ln -sf /usr/bin/mariadb-show /usr/bin/mysqlshow
            ln -sf /usr/bin/mariadb-slap /usr/bin/mysqlslap
            ln -sf /usr/bin/mariadb-tzinfo-to-sql /usr/bin/mysql_tzinfo_to_sql
            ln -sf /usr/bin/mariadb-upgrade /usr/bin/mysql_upgrade
            ln -sf /usr/bin/mariadb-waitpid /usr/bin/mysql_waitpid
            ;;
          *)
            die "Database Engine $DB_ENGINE not supported."
            ;;
        esac
        ;;
      *)
        die "Unsupported Ubuntu version: $OS_VERSION."
        ;;
    esac
  else
    die "Unsupported OS: $OS_NAME."
  fi
}

setupCloudPanel()
{
  DEBIAN_FRONTEND=noninteractive apt -o Dpkg::Options::="--force-overwrite" install -y cloudpanel
  local CLP_RUNNING=$(lsof -u^root -i:8443 -P -n -sTCP:LISTEN)
  if [ -z "${CLP_RUNNING}" ]; then
    die "${RED_TEXT_COLOR}CloudPanel couldn't be installed. Check the log above for errors that occurred in the install process.${RESET_TEXT_COLOR}"
  fi
  showSuccessMessage
}

showSuccessMessage()
{
  CLOUDPANEL_URL="https://$IP:8443"
  printf "\n\n"
  printf "~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~\n"
  printf "${GREEN_TEXT_COLOR}The installation of CloudPanel is complete!${RESET_TEXT_COLOR}\n\n"
  printf "CloudPanel can be accessed now:${YELLOW_TEXT_COLOR} $CLOUDPANEL_URL ${RESET_TEXT_COLOR}\n"
  printf "~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~\n"
}

cleanUp()
{
  history -c
  apt clean
}

setOSInfo
checkRequirements
setIp
setupRequiredPackages
generateLocales
removeUnnecessaryPackages
installMySQL
setupCloudPanel
cleanUp