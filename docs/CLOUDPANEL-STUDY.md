# CloudPanel architecture deep-dive — and the v0.2.48 refactor it demands

**Why this study exists:** a site (`a.garuda.sh`) ended up with nginx
serving from `/home/a-4zwq/...` while its PHP-FPM pool ran as `a-ukfs`.
Different Linux users. Empty 200 forever. The fix is not "patch the
delete path." The fix is fixing the *architecture* that let two parts
of one site disagree on who owned it. CloudPanel's deb shows exactly
how to make that impossible.

This doc traces CloudPanel's site-create flow end to end against the
actual extracted PHP (obfuscated but readable past `goto` flattening
and hex-encoded strings) and maps it line-by-line onto what we need
to change in [internal/site/](../internal/site/),
[internal/webserver/](../internal/webserver/), and
[internal/phpruntime/](../internal/phpruntime/).

Source: `cloudpanel_2.5.3-1+clp-trixie_all.deb` (69 MB, extracted to
`/tmp/clp/extracted/`) plus the public
[`cloudpanel-io/vhost-templates`](https://github.com/cloudpanel-io/vhost-templates)
repo (`v2-http3` branch, 28 templates).

---

## The mental model in one paragraph

A CloudPanel site is **one `Site` entity in memory** + **one transactional
pipeline of Commands** that writes the on-disk artifacts derived from
that entity. Every artifact (nginx vhost, FPM pool, SSL cert files,
logrotate file, docroot, user's home) reads its values from the same
entity field, in the same pass, through the same `CommandExecutor`. The
`siteUser` field flows into:
- `useradd <siteUser>` (CreateUserCommand)
- `root /home/<siteUser>/htdocs/<domain>;` in the nginx vhost (RootDirectory processor)
- `user = <siteUser>` and `group = <siteUser>` in the FPM pool (PoolBuilder)
- `chown -R <siteUser>:<siteUser> /home/<siteUser>` (ChownCommand)

If the entity says `a-ukfs`, every artifact says `a-ukfs`. **There is no
"the nginx vhost lives in one place, the FPM pool in another, and they
need to be kept in sync."** They're rendered from the same source in
the same function call. Drift is structurally impossible.

That's what we're missing.

---

## Layer 1 — the Site entity (single source of truth)

`/tmp/clp/extracted/data/tmp/cloudpanel/data/cloudpanel/files/src/Entity/Site.php`
(symfony Doctrine entity). Stored in SQLite at
`/home/clp/htdocs/app/data/db.sq3`. Fields used during site creation:

| Field | Source | Used by |
|---|---|---|
| `type` | UI choice (`php`, `nodejs`, `python`, `static`, `reverseproxy`) | Creator-subclass dispatch |
| `domainName` | UI input | `server_name`, vhost filename, pool name, cert filename |
| `subdomain` | parsed from `domainName` | `server_name` (adds `www.` for apex) |
| `registrableDomain` | parsed from `domainName` | `server_name` |
| `user` | UI input | useradd, vhost `root`, pool `user`/`group`, chown |
| `userPassword` | UI input (or generated) | useradd `-p` |
| `rootDirectory` | `{template.rootDirectory}/{domain}` | mkdir, vhost `root` |
| `vhostTemplate` | string (raw template body, copied from `VhostTemplate.template`) | render input |
| `application` | `VhostTemplate.name` (`'WordPress'`, `'Generic'`, …) | UI label only |
| `phpSettings.phpVersion` | UI input | pool path `/etc/php/<v>/fpm/pool.d/`, FPM reload target |
| `phpSettings.poolPort` | computed by `PoolReader` at create time | `fastcgi_pass 127.0.0.1:<port>` |
| `phpSettings.memoryLimit / uploadMax / postMax / maxExecutionTime / maxInputVars` | UI defaults from template, editable | pool `php_admin_value[...]` lines via PhpSettings processor |
| `certificate.privateKey`, `certificate.certificate` | OpenSSL self-signed at create time | written to `/etc/nginx/ssl-certificates/<domain>.{key,crt}` |
| `varnishCache`, `varnishCacheSettings` | template default JSON, toggleable | `{{varnish_proxy_pass}}` substitution |

The entity also has `tags`, `application`, `createdAt` — UI-only, not
used during the filesystem pass.

The crucial property: **the entity is validated FIRST** (Symfony Validator
runs `validate()` on `Site` and `PhpSettings`). Constraint errors abort
before any filesystem mutation. We don't always do this — some auraCP
endpoints write the vhost before validating the domain name, leaving a
half-broken site if validation fails late. **Refactor target #1: validate
the whole entity upfront, write nothing if invalid.**

---

## Layer 2 — vhost template + placeholder substitution

### 2a. Template storage

Templates are imported once (`clpctl vhost-templates:import`) from
[cloudpanel-io/vhost-templates](https://github.com/cloudpanel-io/vhost-templates)
and stored as rows in the SQLite `vhost_template` table:

```
(name, type, template, phpVersion, rootDirectory, varnishCacheSettings)
('WordPress', 'system', '<the full nginx config>', '8.3', '', '{"cacheLifetime":...}')
('Generic',   'system', '<config>', '8.3', '', '{"cacheLifetime":...}')
('Nodejs',    'system', '<config>', NULL,  '', NULL)
…
```

The first line of each template file is a `#{...}` nginx-comment-wrapped
JSON metadata header parsed at import:

```nginx
#{"rootDirectory":"wordpress","phpVersion":"8.3","varnishCacheSettings":{"cacheLifetime":"604800","controller":"wordpress",...}}
server {
  listen 80;
  ...
}
```

`rootDirectory: "wordpress"` means "when a site is created from this
template, the actual docroot is `/home/<user>/htdocs/<domain>/wordpress`"
— useful for templates where the app lives in a subdir (e.g. Laravel's
`public/`, Symfony's `public/`, etc.). Empty string = docroot is the
domain dir itself.

### 2b. Template placeholders (the contract)

Every template can reference these `{{placeholders}}`. The Template
subclass for the site type registers exactly the processors it needs;
unmatched placeholders are stripped by `removeEmptyPlaceholders()` as
a final cleanup pass.

| Placeholder | Emitted as | Processor |
|---|---|---|
| `{{server_name}}` | `server_name a.garuda.sh www.a.garuda.sh;` | ServerName |
| `{{root}}` | `root /home/a-ukfs/htdocs/a.garuda.sh;` | RootDirectory |
| `{{ssl_certificate}}` | `ssl_certificate /etc/nginx/ssl-certificates/a.garuda.sh.crt;` | SslCertificate |
| `{{ssl_certificate_key}}` | `ssl_certificate_key /etc/nginx/ssl-certificates/a.garuda.sh.key;` | SslCertificateKey |
| `{{nginx_access_log}}` | `access_log /home/a-ukfs/logs/nginx/access.log;` | NginxAccessLog |
| `{{nginx_error_log}}` | `error_log /home/a-ukfs/logs/nginx/error.log;` | NginxErrorLog |
| `{{php_fpm_port}}` | raw integer, e.g. `9012` (filled into `fastcgi_pass 127.0.0.1:9012`) | PhpFpmPort |
| `{{php_settings}}` | semicolon-joined `key=value\nkey2=value2` string used in `fastcgi_param PHP_VALUE "..."` | PhpSettings |
| `{{settings}}` | operator's freeform additions — the Vhost-tab textarea body | Settings |
| `{{varnish_proxy_pass}}` | `proxy_pass http://127.0.0.1:8080;` (no varnish) **or** `proxy_pass http://127.0.0.1:6081;` (varnish) | VarnishProxyPass |
| `{{redirect_server_name}}` | for redirect template (www→apex) | RedirectServerName |
| `{{redirect_domain}}` | for redirect template (target) | RedirectDomain |
| `{{reverse_proxy_url}}` | `https://upstream.example.com/` | ReverseProxyUrl |
| `{{app_port}}` | Node/Python loopback port | NodejsAppPort / PythonAppPort |

### 2c. A processor, decoded

The obfuscation strips, the logic doesn't:

```php
// src/Site/Nginx/Vhost/Processor/RootDirectory.php (decoded)
class RootDirectory extends Processor {
  protected string $placeholder = '{{root}}';
  public function process(string $content): string {
    $siteUser = $this->site->getUser();
    $rootDirectory = $this->site->getRootDirectory();
    $placeholderValue = sprintf('root /home/%s/htdocs/%s;', $siteUser, $rootDirectory);
    return $this->replace($placeholderValue, $content);
  }
}
```

That's the entire class. The parent `Processor` does the `str_replace`.
Every processor is single-responsibility — one placeholder, one source
field, one substitution. Adding a new placeholder is a new class.

### 2d. Template subclasses

```php
// src/Site/Nginx/Vhost/PhpTemplate.php (effectively)
class PhpTemplate extends Template {
  protected function registerProcessors(): void {
    $this->addProcessor(new ServerName($this->site));
    $this->addProcessor(new RootDirectory($this->site));
    $this->addProcessor(new SslCertificate($this->site));
    $this->addProcessor(new SslCertificateKey($this->site));
    $this->addProcessor(new NginxAccessLog($this->site));
    $this->addProcessor(new NginxErrorLog($this->site));
    $this->addProcessor(new Settings($this->site));
    $this->addProcessor(new VarnishProxyPass($this->site));
    $this->addProcessor(new PhpFpmPort($this->site));
    $this->addProcessor(new PhpSettings($this->site));
  }
}

class NodejsTemplate extends Template {
  protected function registerProcessors(): void {
    $this->addProcessor(new ServerName($this->site));
    $this->addProcessor(new RootDirectory($this->site));
    $this->addProcessor(new SslCertificate($this->site));
    $this->addProcessor(new SslCertificateKey($this->site));
    $this->addProcessor(new NginxAccessLog($this->site));
    $this->addProcessor(new NginxErrorLog($this->site));
    $this->addProcessor(new Settings($this->site));
    $this->addProcessor(new NodejsAppPort($this->site));
    // NO PhpFpmPort, NO PhpSettings, NO VarnishProxyPass
  }
}
```

Each subclass picks exactly the processors that match its template's
`{{placeholders}}`. The base `Template::build()` runs them in order:

```php
public function build(): void {
  $this->registerProcessors();
  foreach ($this->processors as $p) {
    $this->content = $p->process($this->content);
  }
}

public function removeEmptyPlaceholders(): void {
  $this->content = preg_replace('/{{[a-z_]+}}/', '', $this->content);
}
```

---

## Layer 3 — PHP-FPM pool template + port allocator

### 3a. The pool template body

Decoded from `src/Site/PhpFpm/PoolBuilder.php`:

```ini
[{{name}}]
listen = 127.0.0.1:{{port}}
user = {{user}}
group = {{group}}
listen.allowed_clients = 127.0.0.1
pm = ondemand
pm.max_children = 250
pm.process_idle_timeout = 10s
pm.max_requests = 100
listen.backlog = 65535
pm.status_path = /status
request_terminate_timeout = 7200s
rlimit_files = 131072
rlimit_core = unlimited
catch_workers_output = yes
```

Filled with `name=<domain>`, `port=<allocated port>`, `user=<siteUser>`,
`group=<siteUser>`.

Key differences vs auraCP today:

- **TCP loopback (`127.0.0.1:<port>`) not Unix socket.** The vhost ships
  `fastcgi_pass 127.0.0.1:9012;` — no socket path to keep in sync. PHP
  version flip = pool file moves between `/etc/php/8.3/fpm/pool.d/`
  and `/etc/php/8.4/fpm/pool.d/`, but the port stays the same in the
  vhost (which is the FPM contract that didn't break). Our v0.2.47 PHP
  version switch fix wouldn't have been needed in this model.
- **`listen.allowed_clients = 127.0.0.1`** — only loopback can connect
  to a pool. Mitigates the "any local process can hit FPM" concern
  that's the usual TCP-socket downside.
- **`pm.max_children = 250`** — they're optimistic about per-FPM-worker
  RAM (~30 MB ≈ 7.5 GB max per pool). We default to 10.
- **`pm.status_path = /status`** — every pool exposes a per-domain
  metrics endpoint. Free for the panel UI to pull req/s, slow request
  count, etc. without a separate exporter.
- **`catch_workers_output = yes`** — fatal errors land in the FPM log
  (not lost to stderr). Would have caught our `a.garuda.sh` empty-body
  problem in the panel logs immediately.

### 3b. Port allocation (filesystem-derived)

```php
// src/Site/Creator/PhpSite.php :: createPhpFpmPool() (decoded)
public function createPhpFpmPool(): void {
  $siteUser    = $this->site->getUser();
  $phpSettings = $this->site->getPhpSettings();
  $phpVersion  = $phpSettings->getPhpVersion();
  $domainName  = $this->site->getDomainName();

  // 1. SCAN the existing pool directory, parse every pool file
  $poolDirectory = "/etc/php/{$phpVersion}/fpm/pool.d/";
  $poolReader    = new PoolReader($poolDirectory);
  $pools         = $poolReader->getPools();

  // 2. SORT by port desc, take the max, +1
  usort($pools, fn($a, $b) => $a->getPort() < $b->getPort());
  $latestPool = array_shift($pools);
  $poolPort   = $latestPool->getPort() + 1;

  // 3. PERSIST the port BACK onto PhpSettings entity. This is critical:
  //    the vhost render (later in the create pipeline) reads
  //    $phpSettings->getPoolPort() via the PhpFpmPort processor.
  $phpSettings->setPoolPort($poolPort);

  // 4. BUILD the pool config
  $pool = new Pool();
  $pool->setName($domainName);
  $pool->setUser($siteUser);
  $pool->setGroup($siteUser);
  $pool->setPort($poolPort);
  $poolContent = (new PoolBuilder())->create($pool);

  // 5. WRITE to /etc/php/<v>/fpm/pool.d/<domain>.conf
  $poolFile = "/etc/php/{$phpVersion}/fpm/pool.d/{$domainName}.conf";
  $cmd = new WriteFileCommand();
  $cmd->setFile($poolFile);
  $cmd->setContent($poolContent);
  $this->commandExecutor->execute($cmd);
}
```

**Filesystem-derived port allocation** is the right call: self-healing
against manual deletions, no DB counter to get out of sync, no race when
two sites are created concurrently (the second one parses files
including the first one's pool).

The seed pool sits at port 9000 (likely from CloudPanel's first install
boot). New sites get 9001, 9002, 9003 …

---

## Layer 4 — the Creator pipeline

### 4a. Abstract base — `src/Site/Creator.php`

Universal across all site types:

```php
abstract class Creator {
  const NGINX_VHOST_DIRECTORY = '/etc/nginx/sites-enabled/';
  const NGINX_SSL_CERTIFICATES_DIRECTORY = '/etc/nginx/ssl-certificates/';
  const LOGROTATE_DIRECTORY = '/etc/logrotate.d/';

  protected Site $site;
  protected CommandExecutor $commandExecutor;

  public function createUser(): void {
    $user = $this->site->getUser();
    $pwd  = $this->site->getUserPassword();
    $home = "/home/{$user}";
    $skel = realpath(__DIR__ . '/../../resources/etc/skel/site-user/');
    $cmd  = new CreateUserCommand();
    $cmd->setUserName($user);
    $cmd->setPassword($pwd);
    $cmd->setShell('/bin/bash');
    $cmd->setSkeletonDirectory($skel);      // useradd -k <skel>
    $cmd->setHomeDirectory($home);
    $cmd->createHomeDirectory(true);        // useradd -m
    $this->commandExecutor->execute($cmd);
  }

  public function createRootDirectory(): void {
    $cmd = new CreateDirectoryCommand();
    $cmd->setDirectory($this->getRootDirectory());
    $this->commandExecutor->execute($cmd);
  }

  protected function getRootDirectory(): string {
    $user = $this->site->getUser();
    $rd   = $this->site->getRootDirectory();  // already includes template suffix
    return sprintf('/home/%s/htdocs/%s', $user, $rd);
  }

  public function createNginxVhost(): void {
    $domain  = $this->site->getDomainName();
    $rawTmpl = $this->site->getVhostTemplate();   // template body

    // Build per-type Template object that registers its own processors
    switch ($this->site->getType()) {
      case TYPE_PHP:           $tmpl = new PhpTemplate($this->site); break;
      case TYPE_NODEJS:        $tmpl = new NodejsTemplate($this->site); break;
      case TYPE_STATIC:        $tmpl = new StaticTemplate($this->site); break;
      case TYPE_PYTHON:        $tmpl = new PythonTemplate($this->site); break;
      case TYPE_REVERSE_PROXY: $tmpl = new ReverseProxyTemplate($this->site); break;
    }
    $tmpl->setContent($rawTmpl);
    $tmpl->build();                        // run all processors
    $tmpl->removeEmptyPlaceholders();      // strip unmatched {{xxx}}
    $rendered = $tmpl->getContent();

    $file = sprintf('%s/%s.conf', rtrim(self::NGINX_VHOST_DIRECTORY, '/'), $domain);
    $cmd  = new WriteFileCommand();
    $cmd->setFile($file);
    $cmd->setContent($rendered);
    $this->commandExecutor->execute($cmd);
  }

  public function createLogrotateFile(): void {
    $user = $this->site->getUser();
    $tmpl = file_get_contents(realpath(__DIR__ . '/../resources/etc/logrotate/template'));
    $content = str_replace(['{{user}}', '{{group}}'], [$user, $user], $tmpl);
    $file = sprintf('%s/%s', rtrim(self::LOGROTATE_DIRECTORY, '/'), $user);
    $cmd = new WriteFileCommand();
    $cmd->setFile($file);
    $cmd->setContent($content);
    $this->commandExecutor->execute($cmd);
  }

  public function createPrivateKeyAndCertificate(): void {
    $domain = $this->site->getDomainName();
    $cert   = $this->site->getCertificate();
    $key    = sprintf('%s/%s.key', rtrim(self::NGINX_SSL_CERTIFICATES_DIRECTORY, '/'), $domain);
    $crt    = sprintf('%s/%s.crt', rtrim(self::NGINX_SSL_CERTIFICATES_DIRECTORY, '/'), $domain);
    $this->commandExecutor->execute((new WriteFileCommand())->setFile($key)->setContent($cert->getPrivateKey()));
    $this->commandExecutor->execute((new WriteFileCommand())->setFile($crt)->setContent($cert->getCertificate()));
  }

  public function resetPermissions(): void {
    $user = $this->site->getUser();
    $home = "/home/{$user}";
    $ssh  = "{$home}/.ssh";

    // chown -R user:user /home/user
    $this->commandExecutor->execute(
      (new ChownCommand())->setFile($home)->setRecursive(true)->setUser($user)->setGroup($user), 90
    );
    // find /home/user -type d -exec chmod 770 ... -type f -exec chmod 770 ...
    $this->commandExecutor->execute(
      (new FindChmodCommand())->setFile($home)->setDirectoryChmod(770)->setFileChmod(770), 90
    );
    // find /home/user/.ssh -type d -exec chmod 700 ... -type f -exec chmod 600 ...
    $this->commandExecutor->execute(
      (new FindChmodCommand())->setFile($ssh)->setDirectoryChmod(700)->setFileChmod(600), 90
    );
  }

  public function reloadNginxService(): void { $this->reloadService('nginx'); }

  protected function reloadService(string $name): void {
    if ('dev' === $_ENV['APP_ENV']) return;     // skip reload in dev
    $this->commandExecutor->execute((new ServiceReloadCommand())->setServiceName($name));
  }
}
```

### 4b. Per-type subclass — `src/Site/Creator/PhpSite.php`

Adds the PHP-specific steps:

```php
class PhpSite extends Creator {
  const INDEX_PHP_TEMPLATE = "<?php\n\necho 'Hello World :-)';";

  public function createIndexPhp(): void {
    $rd = $this->getRootDirectory();
    $file = sprintf('%s/index.php', rtrim($rd, '/'));
    $this->commandExecutor->execute(
      (new WriteFileCommand())->setFile($file)->setContent(self::INDEX_PHP_TEMPLATE)
    );
  }

  public function createPhpFpmPool(): void { /* per §3b */ }

  public function createVarnishCacheStructure(array $settings): void {
    // Only runs when site.varnishCache = true.
    // Creates /home/<user>/.varnish-cache/, drops settings.json + controller.php,
    // creates /home/<user>/logs/varnish-cache/purge.log.
  }

  public function reloadPhpFpmService(): void {
    $v = $this->site->getPhpSettings()->getPhpVersion();
    $this->reloadService("php{$v}-fpm");
  }
}
```

### 4c. The orchestrator call order

`SiteAddPhpCommand::execute()` runs the steps in this exact order
(extracted from the decoded `goto`-flattened control flow):

```php
$site    = /* build entity from input + validation */;
$tmpl    = $this->vhostTemplateRepo->findOneByName($vhostTemplate);
$site->setRootDirectory($domain . '/' . $tmpl->getRootDirectory());
$site->setVhostTemplate($tmpl->getTemplate());
$site->setCertificate(/* self-signed via Openssl::createSelfSignedCertificate */);

$validator->validate($site);                        // ABORT before any write if invalid
$validator->validate($site->getPhpSettings());

$creator = new PhpSite($site);
$creator->createUser();                             // useradd
$creator->createRootDirectory();                    // mkdir
$creator->createLogrotateFile();                    // /etc/logrotate.d/<user>
$creator->createIndexPhp();                         // <root>/index.php
$creator->createPrivateKeyAndCertificate();         // /etc/nginx/ssl-certificates/<domain>.{key,crt}
$creator->createPhpFpmPool();                       // /etc/php/<v>/fpm/pool.d/<domain>.conf
$creator->reloadPhpFpmService();                    // systemctl reload php<v>-fpm
if ($site->getVarnishCache()) {
  $creator->createVarnishCacheStructure($varnishSettings);
}
$creator->createNginxVhost();                       // /etc/nginx/sites-enabled/<domain>.conf
$creator->reloadNginxService();                     // systemctl reload nginx
$creator->resetPermissions();                       // chown -R + chmod 770 + .ssh
```

**No rollback.** Failures throw and abort — partial state is acceptable
to them. Their bet: validation catches 95% of failures upfront; the
remaining 5% are I/O failures that the operator can deal with manually
or by re-running the create.

**Other-type Creators** are identical except they replace the PHP-specific
steps with their own:
- `NodejsSite::createSystemdUnit()` instead of `createPhpFpmPool()`
- `PythonSite::createSystemdUnit()` ditto
- `StaticSite` skips both
- `ReverseProxySite` skips both

---

## Layer 5 — the `CommandExecutor` (single point for all I/O)

Every filesystem-modifying operation flows through one class, with
distinct Command objects per action: `CreateUserCommand`, `CreateDirectoryCommand`,
`WriteFileCommand`, `CopyFileCommand`, `ChownCommand`, `FindChmodCommand`,
`ServiceReloadCommand`. The executor knows how to invoke each (some via
`shell_exec`, some via PHP file functions).

Two properties this gives them:

1. **Dev-mode short-circuit**: `if ('dev' === $_ENV['APP_ENV']) return;`
   in `reloadService()` means the dev box can rebuild vhosts without
   actually reloading nginx — useful for testing the template render
   without hitting the live server.
2. **Single test injection point**: stub `CommandExecutor::execute()`
   and the entire pipeline becomes unit-testable in-memory. The decoded
   sources show no test files (they probably keep tests private), but
   the pattern enables it.

---

## What this maps to in auraCP — the v0.2.48 refactor

Our current architecture is right at the broad strokes (per-site Linux
user, per-site FPM pool, nginx vhost template, lego for ACME) but
violates the "single source of truth, one transactional pipeline" rule
in three places that produce the exact bug class we just hit.

### Refactor #1 — replace ad-hoc render with a Template + Processor system

**Today:** [internal/webserver/template.go](../internal/webserver/template.go)
emits the vhost via a single Go `text/template`. The template's data
struct is a flat `webserver.Spec` whose fields we set from the Site
record. Render is one shot, no intermediate processor layer.

**Problem:** when a field changes meaning between releases (e.g. v0.2.45
made `Spec.CertPath/KeyPath` mandatory for HTTPS), every call site that
constructs a `Spec` has to be updated. We've already shipped two bugs
from missing updates ([v0.2.45 — siteRenewCert didn't repopulate
cert paths; v0.2.46 — reapplyWeb didn't either]).

**Refactor:**
- New `internal/webserver/processor/` directory, one file per placeholder:
  `server_name.go`, `root.go`, `ssl_certificate.go`, `ssl_certificate_key.go`,
  `nginx_access_log.go`, `nginx_error_log.go`, `php_fpm_port.go`,
  `php_settings.go`, `settings.go`, `varnish_proxy_pass.go`,
  `redirect_server_name.go`, `redirect_domain.go`, `reverse_proxy_url.go`,
  `app_port.go`. Each implements `Processor` interface:
  ```go
  type Processor interface {
      Process(in string, site *store.Site) string
  }
  ```
- New `internal/webserver/template/` directory, one Go file per site type:
  `php.go`, `nodejs.go`, `python.go`, `static.go`, `reverseproxy.go`.
  Each declares `Processors() []Processor`.
- Template bodies become `.tmpl` files under `internal/webserver/templates/`,
  embedded via `//go:embed`. Same `{{placeholder}}` syntax CloudPanel
  uses (deliberately — operator-portable). Initial set ported from
  CloudPanel's `v2-http3` repo, **stripped of HTTP/3 and Varnish**
  (we don't ship those yet), simplified to single `server{}` (no
  loopback-to-8080 dance).
- New `internal/webserver/render.go::Render(domain, type, site, customSettings string) (string, error)`
  loads the type's template, runs its processors against the Site struct,
  strips unmatched `{{xxx}}`, returns the rendered vhost.

The Spec struct goes away. Every call site that needed Spec now passes
the Site itself.

### Refactor #2 — single Creator pipeline per site type

**Today:** [internal/site/site.go::Create](../internal/site/site.go) does
multiple steps in `Create`, with the actual filesystem writes scattered
across `internal/webserver/webserver.go::ApplyPanelProxy` and
`Reload`, `internal/runtime/runtime.go::execStart`, and
`internal/phpruntime/phpruntime.go::WritePool`. The order is implicit —
spread across multiple callers in `api/siteconfig.go::reapplyWeb`,
`api/extras.go::siteRenewCert`, etc. **This is the structural reason
for the `a-4zwq`/`a-ukfs` mismatch:** the vhost and the pool aren't
written from a single function with one Site in hand, so they can
disagree.

**Refactor:**
- New `internal/site/creator/` directory with one file per site type:
  `php.go`, `nodejs.go`, `python.go`, `static.go`, `reverseproxy.go`.
- Each defines a `Creator` struct with the same methods: `CreateUser`,
  `CreateRootDirectory`, `CreateLogrotateFile`, `CreateIndexPhp`
  (PHP only), `CreateSslCertFiles`, `CreatePhpFpmPool` (PHP only),
  `ReloadPhpFpm` (PHP only), `CreateSystemdUnit` (Node/Python only),
  `CreateNginxVhost`, `ReloadNginx`, `ResetPermissions`.
- A single entrypoint function `creator.RunCreate(s *store.Site) error`
  that calls the right Creator methods in the right order. **One
  function, one site, one in-memory record, every artifact derived
  from it.**
- `Delete` mirrors this with a single `creator.RunDelete(domain string)`
  that sweeps `/etc/nginx/sites-{available,enabled}/<domain>.conf` AND
  `/etc/php/*/fpm/pool.d/<domain>.conf` (all PHP versions, not just the
  recorded one) AND `/home/<old-user>/` if user was renamed AND the
  cert files. **One sweep, no orphans, no possibility of leaving a
  stale pool for the next site that picks the same domain.**

### Refactor #3 — preflight validate before any filesystem write

**Today:** `internal/api/sites.go::createSite` validates the body, then
immediately starts calling out — Linux user creation, dir creation,
etc. interleaved with more validation (e.g. PHP version availability
checked after the user is already created).

**Refactor:** new `internal/site/validate.go::Preflight(s *store.Site) error`
that checks every constraint upfront:
- Domain valid + not already taken (vhost file doesn't exist, pool file
  doesn't exist for ANY PHP version, DB record absent)
- Site user valid + not already taken (no /home/<user>, no /etc/passwd entry)
- PHP version installed (for PHP/WordPress)
- Node/Python version installed (for those types)
- Reverse proxy URL parseable + non-loopback (for ReverseProxy)
- The chosen template exists in the embedded set

If any check fails: return error, write nothing. If all pass: proceed
to the Creator pipeline with confidence.

### Refactor #4 — port allocator from filesystem, not DB

**Today:** Node/Python port allocator reads from a DB counter
([internal/store/ports.go](../internal/store/ports.go)). If the panel
DB is restored from backup or the operator manually deletes a vhost
file, the counter is out of sync.

**Refactor:** mirror CloudPanel's `PoolReader` pattern — scan
`/etc/nginx/sites-enabled/*.conf` for existing `proxy_pass
http://127.0.0.1:<port>` entries, take max + 1. Same for the future
PHP-FPM TCP migration if we go there (we don't have to — see
Refactor #5).

### Refactor #5 — Unix socket OR TCP, decided at install time, never per-site

Historically we mixed — Adminer (removed in v0.3.0 / PR #17) used a
Unix socket on a panel-shared pool; sites use Unix sockets too. With
Adminer gone the panel-shared pool is gone too; sites stay on Unix
sockets per the rule below. CloudPanel uses TCP throughout. Pick one
and stick to it: **stay on Unix socket** but make the socket path
canonical and
fix the version-switch ergonomics by using
`/run/php-fpm/<domain>.sock` (no version in path; old version's pool
file gets swept by Refactor #2's Delete on PHP version change). v0.2.47
already does the sweep, but Refactor #2 generalizes it across all the
artifact lifecycles.

### Refactor #6 — bring back the post-create sanity probe

Add `creator.RunCreate` ends with: `curl -kI --resolve <domain>:443:127.0.0.1 https://<domain>/`.
Assert response is non-empty (>0 bytes) and content-type is one of the
expected values (HTML for PHP/Node/Python/Static/WordPress, anything
for ReverseProxy). If empty body or socket failure: log it, surface
"site created but failed smoke test — see logs" to the UI, and *do
not* mark the site `status=active`. The operator sees the error
immediately instead of discovering it when they curl the domain
themselves later. **The `a.garuda.sh` bug would have been caught
at create time** instead of three days into debugging.

---

## What we are NOT doing

- **HTTP/3 / QUIC** — they ship a custom nginx build for it. We use
  stock `nginx.org` mainline. Wait for QUIC GA upstream.
- **Varnish** — their per-RAM cost is too high for "lightweight". Our
  nginx `fastcgi_cache` covers 95% of the same use case.
- **PHPMyAdmin** — historically we shipped Adminer (smaller, better
  UX); v0.3.0 / PR #17 replaced the bundled Adminer with **Aura DB**,
  a native in-panel console (`/dbadmin/`) that ships zero PHP and is
  audit-logged end to end.
- **ProFTPD** — OpenSSH internal-sftp with chroot is lighter. We keep
  this.
- **`/home/clp/services/nginx/` centralization** — operators expect
  `/etc/nginx`. We keep stock paths.
- **`git clone` vhost templates from a public repo at install time** —
  we embed them in the deb via `//go:embed`. Determinism + offline.

---

## Reference paths (working dir for the refactor)

- Deb: `/tmp/clp/cloudpanel.deb` (69 MB)
- Extracted: `/tmp/clp/extracted/{data,control}/`
- Vhost templates: `/tmp/clp/vhost-templates/v2-http3/` (28 templates)
- Single most useful files to crack open while implementing:
  - `data/tmp/cloudpanel/data/cloudpanel/files/src/Site/Creator.php` — abstract base
  - `data/tmp/cloudpanel/data/cloudpanel/files/src/Site/Creator/PhpSite.php` — PHP subclass
  - `data/tmp/cloudpanel/data/cloudpanel/files/src/Site/PhpFpm/PoolBuilder.php` — pool template + render
  - `data/tmp/cloudpanel/data/cloudpanel/files/src/Site/PhpFpm/PoolReader.php` — port allocator
  - `data/tmp/cloudpanel/data/cloudpanel/files/src/Site/Nginx/Vhost/Processor/*.php` — every processor
  - `vhost-templates/v2-http3/Generic/Generic` — minimal PHP template
  - `vhost-templates/v2-http3/WordPress/WordPress` — WP-specific add-ons
  - `vhost-templates/v2-http3/Nodejs/Nodejs` — Node template
  - `vhost-templates/v2-http3/Static/Static` — Static template
  - `vhost-templates/v2-http3/ReverseProxy/ReverseProxy` — ReverseProxy template

The extract is a working directory for the refactor. Delete when
Refactor #1 ships.
