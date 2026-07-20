import { BrowserRouter, Routes, Route, Navigate } from "react-router-dom"

import { LoginPage } from "@/pages/LoginPage"
import { RacesPage } from "@/pages/RacesPage"

function App() {
  return (
    <BrowserRouter>
      <Routes>
        <Route path="/login" element={<LoginPage />} />
        <Route path="/races" element={<RacesPage />} />
        <Route path="/" element={<Navigate to="/races" replace />} />
      </Routes>
    </BrowserRouter>
  )
}

export default App
