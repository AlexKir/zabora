#### zabora

Zabbix agent for Oracle.

Use zabbix protocol.

Template extend zabora from den_crane
https://www.zabbix.com/forum/zabbix-cookbook/5157-oracle-monitoring?postcount=11

#### Dependencies

oracle client

#### Deploy

Install oracle client:
- oracle-instantclient12.1-basic-12.1.0.2.0-1.x86_64.rpm
For build:
- oracle-instantclient12.1-devel-12.1.0.2.0-1.x86_64.rpm
- oracle-instantclient12.1-sqlplus-12.1.0.2.0-1.x86_64.rpm

Add to /etc/ld.so.conf
 /usr/lib/oracle/12.1/client64/lib
 
- Create directory /etc/zabbix/zabora
- Copy zabora zabora.cfg zabora.sql.xml to /etc/zabbix/zabora
- Copy zabora.service to /usr/lib/systemd/system/
- Create user in oracle database for zabora (example create_zabbix_user.sql)
- Add database connect configuration to zabora.cfg 
- Import template to zabbix server.
- Create host to zabbix server (default port for zabora 20050)
  - Set Macro {$ORACLE_SID} for host. {$ORACLE_SID}="TST2" => [DBCFG "TST2"]



 





