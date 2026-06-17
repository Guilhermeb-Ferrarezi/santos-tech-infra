# Backup & Restore — Postgres e Redis

Procedimentos de backup/restore dos dois estados persistentes do ecossistema: o
**Postgres compartilhado** (dados de negócio) e o **Redis** (sessões efêmeras, OTP,
rate limit). Em produção ambos são gerenciados pela Coolify; o `docker-compose.yml`
da raiz é só o stack de dev.

> **A persistência do Redis foi habilitada (AOF).** Antes o Redis rodava sem disco
> e perdia tudo num restart. Agora há durabilidade — mas durabilidade só vale se o
> **backup for testado periodicamente** (ver "Teste de restore").

## Postgres

### Backup (`pg_dump`)

Dump lógico do banco inteiro, formato custom (`-Fc`) — comprimido e restaurável
seletivamente por tabela.

```bash
# Variáveis: ajuste host/porta/usuário/banco ao ambiente (ver memória coolify-infra-apps).
export PGHOST=... PGPORT=5432 PGUSER=... PGDATABASE=...

# Dump custom comprimido, com timestamp no nome.
pg_dump -Fc --no-owner --no-privileges \
  -f "backup_$(date +%Y%m%d_%H%M%S).dump"
```

Para um dump SQL legível (diff/auditoria), use `-Fp` em vez de `-Fc` (não comprimido).

Como o Postgres é **compartilhado** por vários serviços, o dump do banco inteiro cobre
todos os donos de schema de uma vez (ver `docs/schema-ownership.md`). Se precisar de só
um subconjunto, use `-t 'pay_*'`, `-t 'claude_*'`, etc.

### Restore (`pg_restore`)

```bash
# Restore para um banco NOVO/vazio (recomendado: valida o dump sem tocar produção).
createdb restauracao_teste
pg_restore --no-owner --no-privileges -d restauracao_teste backup_XXXXXXXX.dump

# Restore seletivo de uma tabela:
pg_restore --no-owner -d destino -t pay_charges backup_XXXXXXXX.dump
```

Restore por cima de um banco existente exige cuidado: prefira `--clean
--if-exists` apenas se você realmente quer dropar e recriar os objetos. **Nunca**
restaure direto em produção sem antes validar num banco descartável.

### Dump SQL puro

```bash
pg_dump -Fp --no-owner -f backup.sql       # gera
psql -f backup.sql DESTINO                  # restaura
```

## Redis

A persistência usa **AOF (Append Only File)**: o Redis registra cada escrita no
`appendonly.aof`, reconstruindo o estado no boot. Complementarmente, o snapshot RDB
pode ser disparado sob demanda com `BGSAVE`.

### Backup

```bash
# 1) Força um snapshot RDB em background (não bloqueia o servidor).
redis-cli -a "$REDIS_PASSWORD" BGSAVE
# Aguarde concluir: rdb_bgsave_in_progress deve voltar a 0.
redis-cli -a "$REDIS_PASSWORD" INFO persistence | grep rdb_bgsave_in_progress

# 2) Copie os arquivos do diretório de dados do Redis (dump.rdb + AOF).
#    Caminho via: CONFIG GET dir
redis-cli -a "$REDIS_PASSWORD" CONFIG GET dir
# Copie dump.rdb e o diretório appendonlydir/ (ou appendonly.aof) desse local.
```

Para garantir um AOF compacto e consistente antes de copiar:

```bash
redis-cli -a "$REDIS_PASSWORD" BGREWRITEAOF
```

### Restore

1. Pare o Redis.
2. Substitua os arquivos no diretório de dados (`dir`) pelos do backup
   (`appendonlydir/`/`appendonly.aof` e/ou `dump.rdb`).
3. Suba o Redis. Com AOF habilitado, ele reconstrói o estado a partir do AOF (o RDB
   só é usado se o AOF estiver ausente/desabilitado).

> O Redis aqui guarda só estado **efêmero** (sessões, OTP, rate limit). Perdê-lo
> desloga usuários e zera contadores de rate limit, mas **não** perde dado de negócio
> (isso está no Postgres). O backup do Redis é conveniência/continuidade, não fonte
> de verdade.

## Teste de restore (obrigatório, periódico)

Backup que nunca foi restaurado **não é backup**. Periodicamente (recomendado:
mensal e após qualquer mudança grande de schema):

1. **Postgres:** restaure o último dump num banco descartável (`createdb
   restauracao_teste` + `pg_restore`) e rode uma checagem mínima — `SELECT count(*)`
   nas tabelas críticas (`users`, `sessions`, `pay_charges`, etc.) e confira se as
   migrações de cada serviço aplicariam limpo por cima.
2. **Redis:** suba uma instância isolada apontando o `dir` para uma cópia do backup e
   confirme que ela inicia e responde `PING` + `DBSIZE` coerente.
3. Registre data/resultado do teste. Se o restore falhar, **trate como incidente** —
   o backup está quebrado.

## Onde isso roda em produção

Em produção, Postgres e Redis são serviços gerenciados pela **Coolify**. Os comandos
acima rodam contra os hosts/credenciais do ambiente — obtenha-os pela Coolify (ver
memória `coolify-infra-apps`). Os bancos **não** publicam porta no host; o acesso é
pela rede interna ou via túnel SSH (ver memórias `contabo-ssh-access` /
`observabilidade-wireguard-stack`).
