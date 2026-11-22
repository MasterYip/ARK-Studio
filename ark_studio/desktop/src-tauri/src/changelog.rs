//! "ARK Studio was updated to 2.x.y": the key changes of a version, taken
//! from the repository's CHANGELOG.md (bundled at build time).

/// CHANGELOG.md of the source tree this app was built from.
pub const CHANGELOG: &str = include_str!("../../../../CHANGELOG.md");

/// Longest item shown (characters); longer ones end in "…".
const ITEM_MAX: usize = 160;

/// The first `max` items of the `## [version]` section: each top-level
/// bullet as one line (continuation lines joined, Markdown emphasis and
/// code marks removed). Empty when the version has no section.
pub fn notes(changelog: &str, version: &str, max: usize) -> Vec<String> {
    let version = version.trim().trim_start_matches('v');
    if version.is_empty() {
        return Vec::new();
    }
    let head = format!("## [{version}]");
    let mut lines = changelog.lines();
    if !lines.by_ref().any(|l| {
        let l = l.trim_end();
        l == head || l.starts_with(&format!("{head} "))
    }) {
        return Vec::new();
    }
    let mut items: Vec<String> = Vec::new();
    for line in lines {
        if line.starts_with("## ") {
            break;
        }
        if let Some(rest) = line.strip_prefix("- ") {
            items.push(rest.trim().to_string());
        } else if line.starts_with("  ") && !line.trim_start().starts_with("- ") && !line.trim().is_empty() {
            if let Some(last) = items.last_mut() {
                last.push(' ');
                last.push_str(line.trim());
            }
        }
    }
    items.into_iter().take(max).map(|item| shorten(&plain(&item))).collect()
}

fn plain(text: &str) -> String {
    text.replace("**", "").replace('`', "")
}

fn shorten(text: &str) -> String {
    if text.chars().count() <= ITEM_MAX {
        return text.to_string();
    }
    let cut: String = text.chars().take(ITEM_MAX).collect();
    let cut = cut.rsplit_once(' ').map(|(a, _)| a.to_string()).unwrap_or(cut);
    format!("{}…", cut.trim_end_matches([',', ';', ':', '.']))
}

#[cfg(test)]
mod tests {
    use super::*;

    const LOG: &str = "# Changelog\n\n## [Unreleased]\n\n- Not yet.\n\n## [2.1.2] - 2026-11-22\n\n### Added\n\n- Settings > About: **version** and updates\n  in one place.\n- The `ark-server` version.\n  - a nested detail\n\n### Fixed\n\n- A fix.\n\n## [2.1.1] - 2026-11-21\n\n- Older.\n";

    #[test]
    fn takes_the_bullets_of_one_version() {
        assert_eq!(
            notes(LOG, "2.1.2", 5),
            vec!["Settings > About: version and updates in one place.", "The ark-server version.", "A fix."]
        );
        assert_eq!(notes(LOG, "v2.1.2", 2).len(), 2);
        assert_eq!(notes(LOG, "2.1.1", 5), vec!["Older."]);
        assert!(notes(LOG, "9.9.9", 5).is_empty());
        assert!(notes(LOG, "", 5).is_empty());
        // "2.1.1" must not match a "2.1.10" heading
        assert!(notes("## [2.1.10]\n\n- x\n", "2.1.1", 5).is_empty());
    }

    #[test]
    fn long_items_are_shortened_at_a_word() {
        let long = format!("## [1.0.0]\n\n- {}\n", "word ".repeat(60));
        let item = &notes(&long, "1.0.0", 1)[0];
        assert!(item.ends_with('…') && item.chars().count() <= ITEM_MAX + 1);
    }

    #[test]
    fn the_bundled_changelog_is_readable() {
        assert!(CHANGELOG.contains("## [Unreleased]"));
        assert!(!notes(CHANGELOG, "2.1.1", 6).is_empty());
    }
}
