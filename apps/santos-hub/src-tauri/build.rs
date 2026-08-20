use std::{env, fs, path::PathBuf};

fn main() {
    tauri_build::build();
    // `bundle.resources` em tauri.conf.json empacota "target/<profile>/WebView2Loader.dll",
    // mas nada copia esse arquivo pra lá sozinho — webview2-com-sys só extrai a DLL pra
    // dentro do PRÓPRIO OUT_DIR dela (target/<profile>/build/webview2-com-sys-<hash>/out/<arch>),
    // não pro target/<profile> que o bundler espera. Sem isso, `tauri build` falha com
    // "resource path target\release\WebView2Loader.dll doesn't exist".
    if let Err(err) = copy_webview2_loader() {
        println!("cargo:warning=não foi possível copiar WebView2Loader.dll: {err}");
    }
}

fn copy_webview2_loader() -> Result<(), String> {
    let target_dir = target_dir_from_out_dir()?;
    let dest = target_dir.join("WebView2Loader.dll");
    if dest.exists() {
        return Ok(());
    }
    let src = find_webview2_loader_dll()?;
    fs::copy(&src, &dest)
        .map_err(|e| format!("copiar {} -> {}: {e}", src.display(), dest.display()))?;
    println!("cargo:rerun-if-changed={}", src.display());
    Ok(())
}

/// OUT_DIR é algo como target/<profile>/build/<pkg>-<hash>/out — sobe 3 níveis
/// pra chegar em target/<profile>, onde o bundler procura o resource.
fn target_dir_from_out_dir() -> Result<PathBuf, String> {
    let out_dir = PathBuf::from(env::var("OUT_DIR").map_err(|e| e.to_string())?);
    out_dir
        .ancestors()
        .nth(3)
        .map(PathBuf::from)
        .ok_or_else(|| format!("OUT_DIR inesperado: {}", out_dir.display()))
}

/// A crate webview2-com-sys embute os binários prontos em `<crate>/<arch>/WebView2Loader.dll`
/// (sem expor `links=`/`DEP_*`), então procuramos direto no cache do cargo registry — sem
/// depender do número exato da versão (evita quebrar a cada bump de dependência).
fn find_webview2_loader_dll() -> Result<PathBuf, String> {
    let arch = match env::var("CARGO_CFG_TARGET_ARCH").as_deref() {
        Ok("x86_64") => "x64",
        Ok("x86") => "x86",
        Ok("aarch64") => "arm64",
        other => return Err(format!("arquitetura não mapeada: {other:?}")),
    };
    let cargo_home = env::var("CARGO_HOME")
        .map(PathBuf::from)
        .or_else(|_| env::var("USERPROFILE").map(|h| PathBuf::from(h).join(".cargo")))
        .map_err(|e| e.to_string())?;
    let registry_src = cargo_home.join("registry").join("src");
    for index_dir in read_dir_ok(&registry_src)? {
        for crate_dir in read_dir_ok(&index_dir)? {
            let name = crate_dir.file_name().unwrap_or_default().to_string_lossy().to_string();
            if !name.starts_with("webview2-com-sys-") {
                continue;
            }
            let candidate = crate_dir.join(arch).join("WebView2Loader.dll");
            if candidate.is_file() {
                return Ok(candidate);
            }
        }
    }
    Err(format!("WebView2Loader.dll não encontrado em {}", registry_src.display()))
}

fn read_dir_ok(path: &PathBuf) -> Result<Vec<PathBuf>, String> {
    fs::read_dir(path)
        .map(|entries| entries.filter_map(|e| e.ok()).map(|e| e.path()).collect())
        .map_err(|e| format!("ler {}: {e}", path.display()))
}
