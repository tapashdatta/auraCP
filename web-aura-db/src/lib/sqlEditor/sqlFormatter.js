// Thin wrapper around sql-formatter. Re-exporting through a dedicated
// module lets Vite produce a separate chunk (the SqlEditor screen
// dynamic-imports this file, so the formatter is never loaded into the
// main chunk).

import { format as fmt } from 'sql-formatter'

/**
 * @param {string} sql
 * @param {'mariadb'|'postgres'} engine
 * @returns {string}
 */
export function format(sql, engine) {
  const language = engine === 'postgres' ? 'postgresql' : 'mariadb'
  try {
    return fmt(sql, { language, tabWidth: 2, keywordCase: 'upper' })
  } catch {
    // Best-effort — formatting must never destroy the user's input.
    return sql
  }
}
