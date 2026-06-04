import jwt from "jsonwebtoken"
import { config } from "./config"

// Token de serviço HS256 (mesmo JWT_SECRET do ecossistema), assumindo a conta admin
// do dono. O agent-go valida o sub e resolve o papel admin pela tabela users — as
// claims são as mesmas do auth central: {sub, iat, exp}.
export function serviceToken(): string {
  const now = Math.floor(Date.now() / 1000)
  return jwt.sign(
    { sub: String(config.ownerUserID), iat: now, exp: now + 600 },
    config.jwtSecret,
    { algorithm: "HS256" },
  )
}
